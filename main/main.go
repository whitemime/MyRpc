package main

import (
	myrpc "MyRpc"
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// 第一天 简易客户端
type Foo struct{}

type Args struct{ Num1, Num2 int }

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

// 启动服务端
func startServer(addr chan string) {
	var foo Foo
	if err := myrpc.Register(&foo); err != nil {
		log.Fatal("register error:", err)
	}
	lis, err := net.Listen("tcp", ":9999")
	if err != nil {
		log.Fatal("network error:", err)
	}
	log.Println("start rpc server on", lis.Addr())
	//启动http
	//服务端劫持http的裸TCP通道，在通道中用自定义的通信方式
	myrpc.HandleHTTP()
	//确保监听成功后，把地址发送给客户端
	addr <- lis.Addr().String()
	_ = http.Serve(lis, nil)
}

func call(addrCh chan string) {
	//log.SetFlags(0)
	//通道用来接收服务端传来的监听成功地址
	//addr := make(chan string)
	//go startServer(addr)
	//先建立tcp连接，发送http的connect请求，(就实现一次握手)，然后正常走rpc
	client, _ := myrpc.DialHTTP("tcp", <-addrCh)
	defer func() {
		_ = client.Close()
	}()
	//尝试建立一个tcp连接，连接到该地址，返回一个连接对象，表示服务端和客户端的连接
	//conn, _ := net.Dial("tcp", <-addr)
	time.Sleep(time.Second)
	// _ = json.NewEncoder(conn).Encode(myrpc.DefaultOption)
	// cc := codec.NewGobCodec(conn)
	//并发了5个rpc调用
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			//客户端调用及发送功能
			if err := client.Call(ctx, "Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
		// h := &codec.Header{
		// 	ServiceMethod: "Foo.Sum",
		// 	Seq:           uint64(i),
		// }
		// _ = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		// _ = cc.ReadHeader(h)
		// var reply string
		// _ = cc.ReadBody(&reply)
		// log.Println("reply:", reply)
	}
	wg.Wait()
}
func main() {
	log.SetFlags(0)
	ch := make(chan string)
	go call(ch)
	startServer(ch)
}
