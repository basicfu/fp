// Command partner 模拟第三方程序：用 AccessKey 签名调用 demo 的 /api/partner/ping。
//
//	go run ./examples/demo/partner -ak FPAK... -sk ...
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/basicfu/fp/sdk/aksign"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8090/api/partner/ping", "要调用的接口")
	ak := flag.String("ak", "", "AccessKey ID")
	sk := flag.String("sk", "", "AccessKey Secret")
	flag.Parse()

	req, err := http.NewRequest(http.MethodGet, *url, nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := aksign.Sign(req, *ak, *sk); err != nil {
		log.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d %s\n", resp.StatusCode, body)
}
