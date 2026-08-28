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

	// ServeWhenReady 内部会先等撤销中继订阅上 Redis 才开始接受连接——这条
	// 编排本身连同"为什么顺序不能反"的完整推导见 grpcapi.Server.ServeWhenReady
	// 的注释。main.go 这一层只负责调用它、并在它返回错误时按现有惯例
	// （与 httpSrv 的 ListenAndServe 一致）记日志、触发关闭。
	go func() {
		if err := grpcSrv.ServeWhenReady(ctx, grpcLis, 10*time.Second); err != nil {
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
