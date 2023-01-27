package memo

import (
	"log"
	"strings"
	"sync"
	"github.com/gofrs/uuid"
)

type entry struct {
	res   result
	ready chan struct{}
	fail chan struct{}
}

// 记录当前正在调用的线程
var invoker map[string]struct{}

type Memo struct {
	f  Func
	mu sync.Mutex
	cache map[string]*entry
}

type Func func(key string) (interface{}, error)

type result struct {
	value interface{}
	err error
}

func New(f Func) *Memo {
	return &Memo{f: f, cache: make(map[string]*entry)}
}

func (memo *Memo) CanCancelGet(key string, done <-chan struct{}) (interface{}, error) {
	// 如果客户发出了终止指令
	// 根据缓存中的数据，查看是否是当前线程正在调用函数
	// 如果自己只是在等待，直接退出
	// 否则，广播这个消息给所有正在等待该指令执行完毕的channel，
	// 让他们重新尝试调用函数
	id, err := uuid.NewV4()
	if err != nil {
		log.Fatal(err)
	}
	url := key
	res := make(chan result)
	key = strings.Join([]string{id.String(), key}, "::")
	go func(key string, res chan<- result) {
		v, err := memo.Get(key)
		ret := result{v, err}
		res <- ret
	}(key, res)
	select {
	case ret := <-res:
		return ret.value, ret.err
	case <-done:
		e, ok := memo.cache[url]
		// 如果没有找到对应url正在执行的操作
		if !ok || e == nil {
			return nil, nil
		} else {
			// 广播操作撤回消息
			close(e.fail)
			delete(memo.cache, url)
			delete(invoker, id.String())
			return nil, nil
		}
	}
}

func (memo *Memo) Get(key string) (interface{}, error) {
	// 图省事，直接把id和url打包起来传了
	arr := strings.Split(key, "::")
	id := arr[0]
	key = arr[1]
	memo.mu.Lock()
	e := memo.cache[key]
	if e == nil {
		// 如果获取到的数据为空，代表这是第一次调用
		// 正在调用的情况下，e不为空，因为里面有一个channel
		// 调用完毕的情况下，e里就是数据
		e = &entry{ready: make(chan struct{})}
		memo.cache[key] = e
		// 数据的情况修改完成，释放锁，让goroutine去执行这个很慢的函数
		invoker[id] = struct{}{}
		// 标记即将进行访问的线程id
		memo.mu.Unlock()
		e.res.value, e.res.err = memo.f(key)
		close(e.ready)
	} else {
		memo.mu.Unlock()
		// 等待函数执行完毕，在channel关闭之前会被一直阻塞
		// 如果函数正常结束，直接返回对应的返回值
		// 如果函数失败，则等待者重新开始争夺
		// TODO: 如果一个操作多次失败，很有可能造成等待者函数栈过大，可能的解决方案是添加超时机制
		select {
		case <-e.ready:
			return e.res.value, e.res.err
		case <-e.fail:
			return memo.Get(key)
		}
	}
	return e.res.value, e.res.err
}
