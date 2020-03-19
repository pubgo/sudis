package tcp

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ihaiker/gokit/logs"
	"github.com/ihaiker/gokit/remoting"
	"github.com/ihaiker/gokit/remoting/rpc"
	"github.com/ihaiker/sudis/conf"
	"github.com/ihaiker/sudis/daemon"
	"github.com/ihaiker/sudis/master/server"
	"github.com/ihaiker/sudis/master/server/eventbus"
	"strings"
)

var logger = logs.GetLogger("master")

type masterTcpServer struct {
	onShutdown func()
	rpc.RpcServer
	channelManager remoting.ChannelManager
	tails          map[string]daemon.TailLogger
}

func remoteIp(address string) string {
	idx := strings.Index(address, ":")
	return address[0:idx]
}

func NewMasterTcpServer(address string, onShutdown func(), api *server.ApiWrapper) *masterTcpServer {
	masterServer := &masterTcpServer{
		onShutdown: onShutdown,
		tails:      map[string]daemon.TailLogger{},
	}
	masterServer.RpcServer = rpc.NewServer(address, masterServer.onServerMessage, func(channel remoting.Channel) {
		address := remoteIp(channel.GetRemoteAddress())
		key, has := channel.GetAttr("key")
		if has {
			eventbus.Send(eventbus.LostNode(address, key.(string)))
		}
	})
	masterServer.channelManager = NewServerManager()
	api.AddApi(&NodeApi{server: masterServer})
	return masterServer
}

func (self *masterTcpServer) authServer(channel remoting.Channel, request *rpc.Request) *rpc.Response {
	if request.URL == "auth" {
		address := remoteIp(channel.GetRemoteAddress())
		timestamp, exits := request.GetHeader("timestamp")
		key, has := request.GetHeader("key")
		if exits && has {
			code := string(request.Body)
			checkCode := fmt.Sprintf("%x", md5.Sum([]byte(timestamp+conf.Config.Master.SecurityToken)))
			if code == checkCode {
				channel.SetAttr("key", key)
				if key != "sudis.master.console" { //关闭命令使用
					self.channelManager.Add(channel)
					eventbus.Send(eventbus.NewNode(address, key))
				}
				return rpc.OK(channel, request)
			}
		}
		return errorResponse(request, errors.New("NoAuthHeader"))
	}

	if _, has := channel.GetAttr("key"); has {
		return nil
	} else {
		return errorResponse(request, errors.New("Uncertified"))
	}
}

func errorResponse(request *rpc.Request, err error) *rpc.Response {
	resp := rpc.NewResponse(request.ID())
	resp.Error = err
	return resp
}

func (self *masterTcpServer) onServerMessage(channel remoting.Channel, request *rpc.Request) *rpc.Response {
	if resp := self.authServer(channel, request); resp != nil {
		return resp
	}

	address := remoteIp(channel.GetRemoteAddress())
	switch request.URL {
	case "shutdown": //只能本地执行关闭
		if address == "127.0.0.1" {
			eventbus.Send(eventbus.Shutdown())
			self.onShutdown()
			return rpc.OK(channel, request)
		} else {
			return errorResponse(request, errors.New("No permission, reject"))
		}
	case "ProgramStatus":
		args := make([]string, 3)
		_ = json.Unmarshal(request.Body, &args)
		key, _ := channel.GetAttr("key")
		event := &eventbus.ProgramStatusEvent{
			Ip: address, Key: key.(string),
			Name: args[0], OldStatus: args[1], NewStatus: args[2],
		}
		eventbus.Send(eventbus.ProgramStatus(event))
		return rpc.OK(channel, request)
	case "tail.logger":
		id, _ := request.GetHeader("id")
		if tail, has := self.tails[id]; has {
			tail(id, string(request.Body))
		}
	}
	return errorResponse(request, rpc.ErrNotFount)
}

func (self *masterTcpServer) Start() (err error) {
	self.RpcServer.SetChannelManager(self.channelManager)
	err = self.RpcServer.Start()
	return
}

func (self *masterTcpServer) Stop() error {
	logger.Info("master tcp closed")
	return self.RpcServer.Stop()
}
