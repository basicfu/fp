package registry

import "github.com/redis/go-redis/v9"

// 源码单独存一份给 arch_test 用；redis.Script 不暴露源码。
var scriptSources = map[*redis.Script]string{}

func newScript(src string) *redis.Script {
	s := redis.NewScript(src)
	scriptSources[s] = src
	return s
}

func scriptSource(s *redis.Script) string { return scriptSources[s] }

// handshakeScript 在一个 key 上完成：清死节点残留 → 按策略判定 → 登记自己 → 给自己的 field 设 TTL。
// 存活节点列表作为参数传入而不是在脚本里读 fp:im:node：那是另一个 key、另一个槽，Cluster 不允许。
//
// KEYS[1] conn hash
// ARGV[1] connId  ARGV[2] 元数据 JSON  ARGV[3] policy  ARGV[4] limit  ARGV[5] fieldTTL 秒  ARGV[6..] 存活节点
// ARGV[6..] 整体缺失（#ARGV==5）表示调用方没给存活节点信息：跳过死节点清理，把所有既有条目当成活的参与计数，
// 否则"没给信息"会被脚本当成"除自己外都是死的"，把同 subject 下别的节点合法连接误删。
// 返回 {"OK", {connId, nodeId, ...}}（被顶替的连接，扁平列表）或 {"REJECT"}
var handshakeScript = newScript(`
local live = {}
local haveLive = #ARGV > 5
for i = 6, #ARGV do live[ARGV[i]] = true end
local all = redis.call('HGETALL', KEYS[1])
local existing = {}
for i = 1, #all, 2 do
  local cid, meta = all[i], all[i + 1]
  local node = cjson.decode(meta)['n']
  if haveLive and not live[node] then
    redis.call('HDEL', KEYS[1], cid)
  elseif cid ~= ARGV[1] then
    existing[#existing + 1] = cid
    existing[#existing + 1] = node
  end
end
local count = #existing / 2
local policy = ARGV[3]
if policy == 'reject' and count >= 1 then return {'REJECT'} end
if policy == 'limit' and count >= tonumber(ARGV[4]) then return {'REJECT'} end
local kicked = {}
if policy == 'replace' then
  for i = 1, #existing, 2 do
    redis.call('HDEL', KEYS[1], existing[i])
    kicked[#kicked + 1] = existing[i]
    kicked[#kicked + 1] = existing[i + 1]
  end
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('HEXPIRE', KEYS[1], tonumber(ARGV[5]), 'FIELDS', 1, ARGV[1])
return {'OK', kicked}
`)

// kickAllScript 原子地取走并删除整个 hash。返回扁平的 {connId, meta, ...}。
var kickAllScript = newScript(`
local all = redis.call('HGETALL', KEYS[1])
if #all > 0 then redis.call('DEL', KEYS[1]) end
return all
`)

// kickOneScript 原子地取走并删除一个 field。返回 meta 或 false。
var kickOneScript = newScript(`
local meta = redis.call('HGET', KEYS[1], ARGV[1])
if meta then redis.call('HDEL', KEYS[1], ARGV[1]) end
return meta
`)
