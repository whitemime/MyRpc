package myrpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

// 服务的方法结构体
type methodType struct {
	method    reflect.Method //方法
	ArgType   reflect.Type   //客户端传来的参数
	ReplyType reflect.Type   //服务端返回的参数
	numCalls  uint64
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// 创建两个参数的实例
// reflect.Value表示go语言中任意值
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	//判断种类是否为指针类型
	if m.ArgType.Kind() == reflect.Ptr {
		//m.ArgType.Elem()：表示获取指针指向的元素类型
		//reflect.New(...)：创建该类型的新实例（返回的是指针）
		argv = reflect.New(m.ArgType.Elem())
	} else {
		//获取其指向的实际值（解引用）
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}
func (m *methodType) newReply() reflect.Value {
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

// 服务结构体，在go中，一个服务为T.WORK()这样的，所以一个服务也可以是一个结构体
type service struct {
	name   string                 //结构体的名称
	typ    reflect.Type           //结构体的类型
	rcvr   reflect.Value          //结构体的实例
	method map[string]*methodType //映射的结构体的所有符合条件的方法
}

func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	//从实例中获取其名字
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	//判断是否暴露在外
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

// 过滤符合条件的方法
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mtype := method.Type
		//入参必须为3个(第一个是结构体本身，其他两个就是arg和reply)
		//只能返回一个error
		if mtype.NumIn() != 3 || mtype.NumOut() != 1 {
			continue
		}
		if mtype.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		//获取两个入参 判断类型是否是导出类型或内建类型
		argType, replyType := mtype.In(1), mtype.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		//在服务结构体中记录函数名字和其函数实例入参实例的映射
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// 判断类型是否是导出类型或内建类型
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// 调用某个注册的方法m，并传入参数 argv 和 replyv，返回 error 类型的错误
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	//如果这个方法是 func (t *T) Foo(a, b int) error，那么 f 就表示 Foo 这个函数
	f := m.method.Func
	//调用函数 传入服务实例(类似于t),两个入参
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	//返回值只有一个error
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
