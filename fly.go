// Package fly -----------------------------
// @author   : deng zhi
// @time     : 2023/3/31 10:01
// graceful shutdown server
// -------------------------------------------
package fly

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// shutdownPollIntervalMax is the max polling interval when checking
// quiescence during Server.Shutdown. Polling starts with a small
// interval and backs off to the max.
// Ideally we could find a solution that doesn't involve polling,
// but which also doesn't have a high runtime cost (and doesn't
// involve any contentious mutexes), but that is left as an
// exercise for the reader.
const shutdownPollIntervalMax = 500 * time.Millisecond

var logger Logger

var defaultLog = log.New(os.Stdout, "gracefulCloser: ", log.LstdFlags)

// ShutdownSignals receives shutdown signals to process
var ShutdownSignals = []os.Signal{
	os.Interrupt, os.Kill, syscall.SIGKILL, syscall.SIGSTOP,
	syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGILL, syscall.SIGTRAP,
	syscall.SIGABRT, syscall.SIGSYS, syscall.SIGTERM,
}

type Logger interface {
	Print(info string)
}

func CustomLog(customLogger Logger) {
	logger = customLogger
}

type GracefulCloser struct {
	closeFuncChain       map[int][]func(ctx context.Context)
	levelAfterClosedWait map[int]time.Duration
	timeout              time.Duration
	mutex                sync.Mutex
	quit                 chan os.Signal // quit chan os.Signal
}

// NewAndMonitor new a gracefulCloser, and monitor it, timeout: wait timeout for graceful close, stop signal to monitor
func NewAndMonitor(timeout time.Duration, sig ...os.Signal) *GracefulCloser {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	gracefulCloser := &GracefulCloser{
		closeFuncChain:       make(map[int][]func(ctx context.Context)),
		quit:                 make(chan os.Signal, 1),
		levelAfterClosedWait: make(map[int]time.Duration),
		timeout:              timeout,
	}
	if len(sig) == 0 {
		sig = ShutdownSignals
	}
	signal.Notify(gracefulCloser.quit, sig...)
	return gracefulCloser
}

// AddCloser Add closer, closerCoequal is a set of functions with ctx context.Context parameters,
// which needs to be implemented by the user.
// It is usually necessary to implement operations such as closing resources in this function.
// The ctx parameter is used to monitor the super. The user can implement monitoring by himself or not,
// because gracefulCloser will implement a global monitoring,
// @see doShutdown(closeFunS []func (ctx context.Context), ctx context.Context)
// level param represents the priority level of this closer, the smaller the value,
// the higher the priority, and the earlier it will be executed,
// which can solve the scenario when there are interdependent resources for closing
func (closer *GracefulCloser) AddCloser(level int, closerCoequal ...func(ctx context.Context)) *GracefulCloser {
	if closerCoequal == nil {
		// TODO log
		return closer
	}
	closer.mutex.Lock()
	defer closer.mutex.Unlock()

	if value, exists := closer.closeFuncChain[level]; exists {
		closer.closeFuncChain[level] = append(value, closerCoequal...)
	} else {
		closer.closeFuncChain[level] = closerCoequal
	}
	return closer
}

func (closer *GracefulCloser) AddCloserLevelWait(level int, levelWait time.Duration, closerCoequal ...func(ctx context.Context)) *GracefulCloser {
	if closerCoequal == nil {
		// TODO log
		return closer
	}
	closer.mutex.Lock()
	defer closer.mutex.Unlock()

	if value, exists := closer.closeFuncChain[level]; exists {
		closer.closeFuncChain[level] = append(value, closerCoequal...)
	} else {
		closer.closeFuncChain[level] = closerCoequal
	}

	closer.levelAfterClosedWait[level] = levelWait

	return closer
}

// SoftLanding start graceful shutdown
func (closer *GracefulCloser) SoftLanding(done chan<- bool) {
	quit := closer.quit
	if quit == nil {
		panic("Please call the MonitorSignal method to monitor the signal")
	}
	if len(closer.closeFuncChain) == 0 {
		// TODO log closeFuncChain 为空 没有可关闭的函数
		return
	}
	go func() {
		<-quit
		if logger == nil {
			defaultLog.Printf("Server is shutting down...")
		} else {
			logger.Print("Server is shutting down...")
		}

		// Start calling the closer function
		ctx, cancel := context.WithTimeout(context.Background(), closer.timeout)
		defer cancel()

		// Sort according to level, the one with higher priority will be executed first
		levels := sortLevel(closer.closeFuncChain)

		// The smaller the level value, the higher the priority, and execute it first
		for _, level := range levels {
			if logger == nil {
				defaultLog.Printf("Start to execute closeFunc of group %v", level)
			} else {
				logger.Print(fmt.Sprintf("Start to execute closeFunc of group %v", level))
			}

			// execute level closer
			doShutdown(closer.closeFuncChain[level], ctx)

			// wait some time and then execute next level closer
			if waitTime, ok := closer.levelAfterClosedWait[level]; ok {
				time.Sleep(waitTime)
			}
		}

		close(done)
	}()
}

// Sort according to level, the one with higher priority will be executed first
func sortLevel(closeFuncChain map[int][]func(ctx context.Context)) []int {
	levels := make([]int, len(closeFuncChain))
	i := 0
	for key := range closeFuncChain {
		levels[i] = key
		i++
	}
	sort.Ints(levels)
	return levels
}

// Execute the close operation and execute closeFunS concurrently
func doShutdown(closeFunS []func(ctx context.Context), ctx context.Context) {
	// used to wait for the program to complete
	doneCount := atomic.Int32{}
	// Count initialization, which means to wait for len(closeFunS) goroutines
	doneCount.Store(int32(len(closeFunS)))
	for _, closeFun := range closeFunS {
		fun := closeFun
		go func() {
			// Call Done when the function exits to notify the parent that the work is done
			defer func() {
				if err := recover(); err != nil {
					if logger == nil {
						defaultLog.Printf("closeFunc execute panic %v", err)
					} else {
						logger.Print(fmt.Sprintf("closeFunc execute panic:%v", err))
					}
				}
				doneCount.Add(-1)
			}()
			fun(ctx)
		}()
	}

	// Detect context timeout
	pollIntervalBase := time.Millisecond
	timer := time.NewTimer(nextPollInterval(&pollIntervalBase))
	defer timer.Stop()
	for doneCount.Load() > 0 {
		select {
		case <-ctx.Done():
			doLog("Graceful shutdown timeout, forced to end: %v", ctx.Err())
		case <-timer.C:
			timer.Reset(nextPollInterval(&pollIntervalBase))
		}
	}
}

// Rolling timer interval
func nextPollInterval(pollIntervalBase *time.Duration) time.Duration {
	// Add 10% jitter.
	interval := *pollIntervalBase + time.Duration(rand.Intn(int(*pollIntervalBase/10)))
	// Double and clamp for next time.
	*pollIntervalBase *= 2
	if *pollIntervalBase > shutdownPollIntervalMax {
		*pollIntervalBase = shutdownPollIntervalMax
	}
	return interval
}

func doLog(format string, param ...any) {
	if logger == nil {
		defaultLog.Printf(format, param...)
	} else {
		logger.Print(fmt.Sprintf(format, param...))
	}
}
