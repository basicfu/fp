// Package fpsdk 是 fp 的 Go 接入 SDK。
//
// 最小用法：
//
//	client, err := fpsdk.New(fpsdk.Options{
//	    Addr:      "fp.internal:9090",
//	    AppID:     os.Getenv("FP_APP_ID"),
//	    AppSecret: os.Getenv("FP_APP_SECRET"),
//	})
//	defer client.Close()
//	http.Handle("/api/", client.Auth().Middleware(apiHandler))
//
// SDK 跑在业务方进程里，**任何一处 panic 都等于业务方崩溃**，
// 因此本包内不使用 panic，参数错误一律经 error 返回。
//
// 本包不得 import internal/ 下的任何包。Go 的 internal 规则不会拦住
// 同 module 内的引用，编译能过——这条只能靠纪律和评审维持。
package fpsdk
