/**
 * copyToClipboard 尽力把文本写进剪贴板，返回是否成功。
 *
 * 现代 Clipboard API 在非安全上下文（http 且非 localhost）下
 * navigator.clipboard 整个是 undefined，而这个后台经常是内网 http 直接
 * 访问，不是个例外情况——之前的实现只是把这种情况包成一句"复制失败，
 * 请手动选中复制"，按钮形同虚设。这里退化到 document.execCommand('copy')：
 * 借一个不可见的 textarea 让浏览器把当前选区复制走，这个老 API 不要求
 * 安全上下文，只要求在一次真实的用户手势（点击）里同步调用——调用方
 * 不能把这个函数的调用点挪到 await 之后，否则退化路径会因为不在用户
 * 手势里而静默失败。
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 落到下面的兼容方案。
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const textarea = document.createElement('textarea')
  textarea.value = text
  // 挪到视口外但不能用 display:none／隐藏属性——部分浏览器选不中这样的
  // 元素，execCommand('copy') 会静默失败。
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  document.body.appendChild(textarea)
  textarea.focus()
  textarea.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(textarea)
  return ok
}
