package main

import (
	myrpc "MyRpc"
	"MyRpc/codec"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

//第一天 简易客户端

// 启动服务端
func startServer(addr chan string) {
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}
	log.Println("start rpc server on", lis.Addr())
	//确保监听成功后，把地址发送给客户端
	addr <- lis.Addr().String()
	myrpc.Accept(lis)
}

func main() {
	//通道用来接收服务端传来的监听成功地址
	addr := make(chan string)
	go startServer(addr)
	//尝试建立一个tcp连接，连接到该地址，返回一个连接对象，表示服务端和客户端的连接
	conn, _ := net.Dial("tcp", <-addr)
	defer func() {
		_ = conn.Close()
	}()
	time.Sleep(time.Second)
	_ = json.NewEncoder(conn).Encode(myrpc.DefaultOption)
	cc := codec.NewGobCodec(conn)
	for i := 0; i < 5; i++ {
		h := &codec.Header{
			ServiceMethod: "Foo.Sum",
			Seq:           uint64(i),
		}
		_ = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		_ = cc.ReadHeader(h)
		var reply string
		_ = cc.ReadBody(&reply)
		log.Println("reply:", reply)
	}
}
