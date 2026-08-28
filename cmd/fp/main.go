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

	// 短信供应商：生产环境唯一走阿里云，绝不能用 notify.NewFakeProvider——
	// 那只会把验证码留在内存里，谁也收不到短信，而且不会有任何报错。
	// config.Load 已经校验过下面四个字段非空；NewAliyunSMS 这里的错误
	// 只会来自它自己额外的校验（目前没有，未来如果加了新的必填项，
	// 这里同样会让启动失败，而不是带着一个半残的供应商跑起来）。
	smsSender := notify.NewSender(pool, store.NewRateLimiter(rdb), nil)
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
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务异常退出", "err", err)
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

	// grpcRunFailed 只在 Run 未能撑到 ctx 取消、提前带错误退出时才会收到
	// 一个值（缓冲为 1，不会阻塞这里的 goroutine）。Run 正常结束（ctx 取消）
	// 永远返回 nil，什么都不会往这个 channel 里送——"一切正常"这件事已经
	// 由 grpcSrv.Ready() 表达了，不需要 grpcRunFailed 重复表达一遍，这样
	// 下面的 select 也就不必费力区分"收到 nil"和"收到真错误"两种情况。
	grpcRunFailed := make(chan error, 1)
	go func() {
		if err := grpcSrv.Run(ctx); err != nil {
			log.Error("撤销事件中继异常退出", "err", err)
			stop()
			grpcRunFailed <- err
		}
	}()

	// Serve 必须等 hub 真正订阅上 Redis 才能开始接受连接：Watch 一连上就会
	// 发 ready，SDK 把 ready 当作"此后的撤销不会漏推"的承诺（proto
	// WatchReady 的注释）。hub 还没订阅时提前 Serve，恰好撞在这段窗口里的
	// 撤销事件就会无声丢失，SDK 却毫不知情、不会收紧本地缓存窗口——fp
	// 重启时全部 SDK 同时重连，这个窗口最容易被撞上。完整推导见
	// grpcapi.RevokeHub.Ready 的注释。
	//
	// 带超时：Redis 不可达时 Run 会带着错误尽快返回，上面的 goroutine
	// 立刻转发到 grpcRunFailed，这里不必等满超时；10 秒超时是给"迟迟
	// 没有错误也没有 ready"这种不该发生、但不能没有兜底的情况兜底，
	// 避免进程静默挂在启动阶段、日志里却什么也不说。
	select {
	case <-grpcSrv.Ready():
	case err := <-grpcRunFailed:
		return fmt.Errorf("gRPC 撤销事件中继未能就绪: %w", err)
	case <-time.After(10 * time.Second):
		return errors.New("等待 gRPC 撤销事件中继就绪超时")
	}

	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Error("gRPC 服务异常退出", "err", err)
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
	return nil
}
