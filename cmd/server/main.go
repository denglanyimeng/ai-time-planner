// Command server 启动 AI 智能时间规划效率助手服务。
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai-time-planner/internal/ai"
	"ai-time-planner/internal/api"
	"ai-time-planner/internal/config"
	"ai-time-planner/internal/service"
	"ai-time-planner/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags)
	cfg := config.Load()

	// ---------- 数据库 ----------
	db, err := store.Open(cfg.DB)
	if err != nil {
		log.Fatalf(`数据库连接失败：%v

请依次确认：
  1. MySQL 服务正在运行；
  2. 已执行 scripts\init_db.bat（或 scripts\init_db.sql）创建数据库与账号；
  3. 项目根目录存在 .env，且 DB_USER / DB_PASSWORD / DB_NAME 与实际一致。`, err)
	}
	if err := store.Migrate(db); err != nil {
		log.Fatalf("表结构迁移失败：%v", err)
	}
	if err := store.EnsureDefaultTags(db); err != nil {
		log.Printf("[启动] 初始化默认标签失败（不影响使用）：%v", err)
	}

	svc := service.New(db,
		ai.New(cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model, cfg.AI.TimeoutSeconds),
		cfg)

	// ---------- 先独占端口，再做任何会改动数据的操作 ----------
	//
	// 顺序在这里非常关键。启动收尾（SettleAllRunning）会结算所有「进行中」的计时任务，
	// 而它过去被放在监听之前。于是当端口已被占用时（例如用户已经开着一个实例），
	// 这个注定要启动失败、根本没在提供服务的进程，
	// 依然会先把那个正在服务的实例的计时任务全部结算掉 ——
	// 表现出来就是「用户没动它，任务却自己暂停了」。
	//
	// 因此改为：先把端口抢到手，确认自己是唯一实例，之后才允许动数据。
	ln, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		log.Fatalf(`端口 %s 已被占用，无法启动：%v

这通常说明已经有一个实例在运行了 —— 请先看看浏览器能否打开
  http://localhost:%s
如果能打开，直接用那个即可，不必重复启动。

如果确实需要同时再跑一个（例如连的是另一个数据库），
请修改 .env 里的 PORT 换一个端口再启动。`, cfg.Port, err, cfg.Port)
	}

	// 端口已独占，此时才做启动收尾。
	// 服务重启意味着此前这段时间前端不可能在心跳，因此把「进行中」的计时
	// 回退到各自最后一次心跳时刻再结算，避免把服务停机时间也算成学习时长。
	if n, err := svc.SettleAllRunning(); err != nil {
		log.Printf("[启动] 结算历史计时失败：%v", err)
	} else if n > 0 {
		log.Printf("[启动] 已结算 %d 个此前未收尾的计时任务", n)
	}

	// ---------- 后台维护 ----------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.RunJanitor(ctx, time.Minute)

	// ---------- HTTP ----------
	webDir := strings.TrimSpace(os.Getenv("WEB_DIR"))
	if webDir == "" {
		webDir = "web"
	}
	router := api.NewRouter(svc, cfg, webDir)

	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("AI 智能时间规划效率助手已启动：http://localhost:%s", cfg.Port)
		if !svc.AIClient().Configured() {
			log.Printf("提示：尚未配置 AI 接口，计划生成将降级为本地规则模式（可在页面「设置」中配置）")
		}
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出：%v", err)
		}
	}()

	// ---------- 优雅退出 ----------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭服务…")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭时出现异常：%v", err)
	}
	log.Println("服务已停止")
}
