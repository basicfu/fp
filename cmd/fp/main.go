// Command fp 是 Foundation Platform 的服务端进程。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/logging"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp 启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.OpenPostgres(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("数据库迁移完成")

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	sessionStore := store.NewSessionStore(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	epochStore := store.NewEpochStore(rdb)

	userSvc := service.NewUserService(pool)
	appSvc := service.NewApplicationService(pool)
	codeSvc := notify.NewCodeService(rdb)

	registry := connector.NewRegistry()
	if err := registry.Register(connector.NewPassword(userSvc)); err != nil {
		return err
	}
	if err := registry.Register(connector.NewSMSCode(codeSvc)); err != nil {
		return err
	}

	sessionSvc := service.NewSessionService(sessionStore, revokePub, epochStore)

	adminSvc := service.NewAdminService(pool, rdb)
	if err := adminSvc.EnsureBootstrap(ctx, cfg.BootstrapAdminUser, cfg.BootstrapAdminPassword); err != nil {
		return err
	}

	logSvc := service.NewLoginLogService(pool)

	// 短信供应商：四项阿里云凭据齐全就用真实供应商——不管是不是生产环境，
	// 有人就是想在本机联调真实短信通道。凭据不全时：
	//   - 生产环境：config.Load 已经把这四项收进必填校验，走不到这里；
	//     留着下面这个分支纯属防御性代码，防的是"以后有人绕开 Load 直接
	//     构造 Config"这种理论情况。
	//   - 非生产环境：退化成 notify.NewFakeProvider（验证码只进内存，不会
	//     真的发短信）并打一条 WARN——不能悄悄降级，那正是这条约束最初
	//     要防的事，只是"未配置就不允许启动"这个更严格的要求只该套在
	//     生产环境头上，逼所有本机开发和 CI 都先备齐（哪怕是假的）阿里云
	//     凭据才能起服务是过度的。
	smsSender := notify.NewSender(pool, store.NewRateLimiter(rdb), nil)
	aliyunConfigured := cfg.AliyunAccessKeyID != "" && cfg.AliyunAccessKeySecret != "" &&
		cfg.AliyunSMSSignName != "" && cfg.AliyunSMSTemplateLoginCode != ""
	switch {
	case aliyunConfigured:
		aliyunSMS, err := notify.NewAliyunSMS(notify.AliyunConfig{
			AccessKeyID:     cfg.AliyunAccessKeyID,
			AccessKeySecret: cfg.AliyunAccessKeySecret,
			Endpoint:        cfg.AliyunEndpoint,
			SignName:        cfg.AliyunSMSSignName,
			Templates: map[string]string{
				service.LoginCodeTemplate: cfg.AliyunSMSTemplateLoginCode,
			},
		})
		if err != nil {
			return err
		}
		smsSender.AddProvider(aliyunSMS)
	case cfg.IsProd():
		// 理论上到不了这里：config.Load 已经保证生产环境下 aliyunConfigured
		// 必为 true。留作防御性兜底，见上面的注释。
		return errors.New("生产环境缺少阿里云短信凭据")
	default:
		log.Warn("阿里云短信未配置，短信通道使用内存假供应商——验证码不会真的发送，仅限本地开发/测试使用")
		smsSender.AddProvider(notify.NewFakeProvider(notify.ChannelSMS, "fake"))
	}

	authSvc := service.NewAuthService(service.AuthDeps{
		Apps:     appSvc,
		Users:    userSvc,
		Sessions: sessionSvc,
		Logs:     logSvc,
		Registry: registry,
		Notifier: smsSender,
		Codes:    codeSvc,
	})

	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Admin:    adminSvc,
			Apps:     appSvc,
			Users:    userSvc,
			Accounts: service.NewAccountService(userSvc, sessionSvc, epochStore, logSvc),
			Sessions: sessionSvc,
			Logs:     logSvc,
			Registry: registry,
			// 生产环境的管理端 cookie 必须带 Secure。
			SecureCookies: cfg.IsProd(),
		}),
	}

	// fatalErr 收后台服务（HTTP、gRPC）的致命错误。带缓冲，保证两个
	// goroutine 都不会因为没人接收而卡在发送上；容量给到二者各投一次。
	//
	// 存在的理由：下面两个 goroutine 失败时都只是 stop() 触发关闭流程，
	// 不会让 run() 直接返回错误——如果只记日志、不让退出码也反映这类
	// 失败，"fp 重启时 Redis 恰好抖动导致 gRPC 撤销中继一直连不上"这种
	// 场景，从进程管理器的角度看会和一次干净的 SIGTERM 优雅关闭没有任何
	// 区别（都是退出码 0）。systemd/k8s 之类只看退出码决定要不要告警、
	// 要不要重启，这正是"等 Ready 再 Serve"这整套设计要防的场景里最容易
	// 被漏判的一种。
	fatalErr := make(chan error, 2)

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatalErr <- fmt.Errorf("HTTP 服务异常退出: %w", err)
			stop()
		}
	}()

	grpcSrv := grpcapi.New(grpcapi.Deps{
		Auth: authSvc,
		Apps: appSvc,
		Pub:  revokePub,
	})

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("监听 gRPC 地址 %s: %w", cfg.GRPCAddr, err)
	}

	// ServeWhenReady 内部会先等撤销中继订阅上 Redis 才开始接受连接——这条
	// 编排本身连同"为什么顺序不能反"的完整推导见 grpcapi.Server.ServeWhenReady
	// 的注释。
	go func() {
		if err := grpcSrv.ServeWhenReady(ctx, grpcLis, 10*time.Second); err != nil {
			fatalErr <- fmt.Errorf("gRPC 服务异常退出: %w", err)
			stop()
		}
	}()
	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcSrv.Shutdown(shutdownCtx)
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}

	// 区分"收到外部信号的正常退出"与"后台服务把自己搞挂了、靠 stop()
	// 间接触发的退出"：前者返回 nil（退出码 0），后者把错误传出去，
	// main() 会打印"fp 启动失败"并以退出码 1 结束。不能让这两种情况在
	// 退出码上无法区分。
	//
	// 排空而不是只读一次：HTTP 与 gRPC 若同时失败（比如系统性资源耗尽
	// 同时打到两个监听），只读一次会让先写入之外的另一个错误永久丢失——
	// 退出码依然正确，但双重故障恰恰是最需要完整诊断信息的场景。
	//
	// 用非阻塞 select 排空，而不是 close(fatalErr) 再 range：想 close 得
	// 先确定两个 goroutine 都不可能再往里写了——直觉上"两个 Shutdown 都
	// 已经返回"应该意味着这一点，但 grpc-go 的 Serve() 返回与 Shutdown()
	// 内部 GracefulStop()/Stop() 返回，是被同一个内部信号唤醒的两个独立
	// goroutine，彼此谁先返回到自己的调用方没有保证；虽然两者的公开契约
	// 都保证优雅停止后 Serve 返回 nil（这条路径根本不会往 fatalErr 发送，
	// 所以实践中不会真的因为这个时序问题丢错误或 panic），但没有任何
	// 机制能让这里 100% 确信"此刻两个 goroutine 都已经执行过它们的发送
	// 语句"。贸然 close 一个理论上可能还有人在发送的 channel，代价是
	// panic（send on closed channel）——用非阻塞 select 排空，即使真的
	// 漏读了一个理论上极罕见的迟发错误，后果也只是"少了一条诊断信息"，
	// 比"关闭阶段直接 panic 整个进程"安全得多。
	//
	// errors.Join 对空切片返回 nil，正常退出路径的返回值不受影响。
	var errs []error
drain:
	for {
		select {
		case err := <-fatalErr:
			errs = append(errs, err)
		default:
			break drain
		}
	}
	return errors.Join(errs...)
}
