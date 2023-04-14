# 介绍
## 1、**graceful- shutdown提供一个优雅关闭服务的能力**，支持按照优先级（level）分组执行关闭任务，用户可以自定义资源关闭顺序
比如，先关闭http/rpc/mq服务连接，再关闭内部资源连接；   
## 2、同一优先级组的任务采用并发执行，提升关闭效率；  
## 3、支持设置关闭超时时间。避免因未知问题导致无法关闭服务器


# 使用样例
    package main

    import (
    "context"
    "fmt"
    "os"
    "time"

	fly "github.com/onepiece-dz/soft_landing"
    )

    func main() {
        gracefulCloser := fly.NewAndMonitor(200*time.Second, os.Interrupt)
        gracefulCloser.AddCloser(0, func(ctx context.Context) {
            time.Sleep(5 * time.Second)
            fmt.Println("0-one closed")
        })
        gracefulCloser.AddCloser(0, func(ctx context.Context) {
            time.Sleep(5 * time.Second)
            fmt.Println("0-two closed")
        }, func(ctx context.Context) {
            time.Sleep(5 * time.Second)
            fmt.Println("0-three closed")
        })
        gracefulCloser.AddCloser(2, func(ctx context.Context) {
                time.Sleep(5 * time.Second)
                fmt.Println("2-four closed")
        }, func(ctx context.Context) {
                time.Sleep(2 * time.Second)
                fmt.Println("2-five closed")
        })
        gracefulCloser.AddCloser(3, func(ctx context.Context) {
                time.Sleep(5 * time.Second)
                fmt.Println("3-four closed")
        }, func(ctx context.Context) {
                time.Sleep(5 * time.Second)
                fmt.Println("3-five closed")
        })
        done := make(chan bool, 1)
        gracefulCloser.SoftLanding(done)

	    println("do something")

	    start := time.Now() // 获取当前时间
	    <-done
	    elapsed := time.Since(start)
	    fmt.Println("该函数执行完成耗时：", elapsed)
	    time.Sleep(1 * time.Second)
	    println("主程序关闭")
}
