// Copyright 2017 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxy

import (
	"context"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"time"

	libio "github.com/fatedier/golib/io"
	libnet "github.com/fatedier/golib/net"
	"golang.org/x/time/rate"

	"github.com/SianHH/frp-package/pkg/config/types"
	v1 "github.com/SianHH/frp-package/pkg/config/v1"
	"github.com/SianHH/frp-package/pkg/msg"
	plugin "github.com/SianHH/frp-package/pkg/plugin/client"
	"github.com/SianHH/frp-package/pkg/transport"
	"github.com/SianHH/frp-package/pkg/util/limit"
	netpkg "github.com/SianHH/frp-package/pkg/util/net"
	"github.com/SianHH/frp-package/pkg/util/xlog"
	"github.com/SianHH/frp-package/pkg/vnet"
)

var proxyFactoryRegistry = map[reflect.Type]func(*BaseProxy, v1.ProxyConfigurer) Proxy{}

func RegisterProxyFactory(proxyConfType reflect.Type, factory func(*BaseProxy, v1.ProxyConfigurer) Proxy) {
	proxyFactoryRegistry[proxyConfType] = factory
}

// Proxy defines how to handle work connections for different proxy type.
type Proxy interface {
	Run() error
	// InWorkConn accept work connections registered to server.
	InWorkConn(net.Conn, *msg.StartWorkConn)
	SetInWorkConnCallback(func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) /* continue */ bool)
	Close()
}

func NewProxy(
	ctx context.Context,
	pxyConf v1.ProxyConfigurer,
	clientCfg *v1.ClientCommonConfig,
	msgTransporter transport.MessageTransporter,
	vnetController *vnet.Controller,
) (pxy Proxy) {
	var limiter *rate.Limiter
	limitBytes := pxyConf.GetBaseConfig().Transport.BandwidthLimit.Bytes()
	if limitBytes > 0 && pxyConf.GetBaseConfig().Transport.BandwidthLimitMode == types.BandwidthLimitModeClient {
		limiter = rate.NewLimiter(rate.Limit(float64(limitBytes)), int(limitBytes))
	}

	baseProxy := BaseProxy{
		baseCfg:        pxyConf.GetBaseConfig(),
		clientCfg:      clientCfg,
		limiter:        limiter,
		msgTransporter: msgTransporter,
		vnetController: vnetController,
		xl:             xlog.FromContextSafe(ctx),
		ctx:            ctx,
	}

	factory := proxyFactoryRegistry[reflect.TypeOf(pxyConf)]
	if factory == nil {
		return nil
	}
	return factory(&baseProxy, pxyConf)
}

type BaseProxy struct {
	baseCfg        *v1.ProxyBaseConfig
	clientCfg      *v1.ClientCommonConfig
	msgTransporter transport.MessageTransporter
	vnetController *vnet.Controller
	limiter        *rate.Limiter
	// proxyPlugin is used to handle connections instead of dialing to local service.
	// It's only validate for TCP protocol now.
	proxyPlugin        plugin.Plugin
	inWorkConnCallback func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) /* continue */ bool

	mu  sync.RWMutex
	xl  *xlog.Logger
	ctx context.Context
}

func (pxy *BaseProxy) Run() error {
	if pxy.baseCfg.Plugin.Type != "" {
		p, err := plugin.Create(pxy.baseCfg.Plugin.Type, plugin.PluginContext{
			Name:           pxy.baseCfg.Name,
			VnetController: pxy.vnetController,
		}, pxy.baseCfg.Plugin.ClientPluginOptions)
		if err != nil {
			return err
		}
		pxy.proxyPlugin = p
	}
	return nil
}

func (pxy *BaseProxy) Close() {
	if pxy.proxyPlugin != nil {
		pxy.proxyPlugin.Close()
	}
}

func (pxy *BaseProxy) SetInWorkConnCallback(cb func(*v1.ProxyBaseConfig, net.Conn, *msg.StartWorkConn) bool) {
	pxy.inWorkConnCallback = cb
}

func (pxy *BaseProxy) InWorkConn(conn net.Conn, m *msg.StartWorkConn) {
	if pxy.inWorkConnCallback != nil {
		if !pxy.inWorkConnCallback(pxy.baseCfg, conn, m) {
			return
		}
	}
	pxy.HandleTCPWorkConnection(conn, m, []byte(pxy.clientCfg.Auth.Token))
}

// Common handler for tcp work connections.
func (pxy *BaseProxy) HandleTCPWorkConnection(workConn net.Conn, m *msg.StartWorkConn, encKey []byte) {
	xl := pxy.xl
	baseCfg := pxy.baseCfg
	var (
		remote io.ReadWriteCloser
		err    error
	)
	remote = workConn
	if pxy.limiter != nil {
		remote = libio.WrapReadWriteCloser(limit.NewReader(workConn, pxy.limiter), limit.NewWriter(workConn, pxy.limiter), func() error {
			return workConn.Close()
		})
	}

	xl.Tracef("handle tcp work connection, useEncryption: %t, useCompression: %t",
		baseCfg.Transport.UseEncryption, baseCfg.Transport.UseCompression)
	if baseCfg.Transport.UseEncryption {
		remote, err = libio.WithEncryption(remote, encKey)
		if err != nil {
			workConn.Close()
			xl.Errorf("create encryption stream error: %v", err)
			return
		}
	}
	var compressionResourceRecycleFn func()
	if baseCfg.Transport.UseCompression {
		remote, compressionResourceRecycleFn = libio.WithCompressionFromPool(remote)
	}

	// check if we need to send proxy protocol info
	var connInfo plugin.ConnectionInfo
	if m.SrcAddr != "" && m.SrcPort != 0 {
		if m.DstAddr == "" {
			m.DstAddr = "127.0.0.1"
		}
		srcAddr, _ := net.ResolveTCPAddr("tcp", net.JoinHostPort(m.SrcAddr, strconv.Itoa(int(m.SrcPort))))
		dstAddr, _ := net.ResolveTCPAddr("tcp", net.JoinHostPort(m.DstAddr, strconv.Itoa(int(m.DstPort))))
		connInfo.SrcAddr = srcAddr
		connInfo.DstAddr = dstAddr
	}

	if baseCfg.Transport.ProxyProtocolVersion != "" && m.SrcAddr != "" && m.SrcPort != 0 {
		// Use the common proxy protocol builder function
		header := netpkg.BuildProxyProtocolHeaderStruct(connInfo.SrcAddr, connInfo.DstAddr, baseCfg.Transport.ProxyProtocolVersion)
		connInfo.ProxyProtocolHeader = header
	}
	connInfo.Conn = remote
	connInfo.UnderlyingConn = workConn

	if pxy.proxyPlugin != nil {
		// if plugin is set, let plugin handle connection first
		xl.Debugf("handle by plugin: %s", pxy.proxyPlugin.Name())
		pxy.proxyPlugin.Handle(pxy.ctx, &connInfo)
		xl.Debugf("handle by plugin finished")
		return
	}

	localConn, err := libnet.Dial(
		net.JoinHostPort(baseCfg.LocalIP, strconv.Itoa(baseCfg.LocalPort)),
		libnet.WithTimeout(10*time.Second),
	)
	if err != nil {
		workConn.Close()
		xl.Errorf("connect to local service [%s:%d] error: %v", baseCfg.LocalIP, baseCfg.LocalPort, err)
		return
	}

	xl.Debugf("join connections, localConn(l[%s] r[%s]) workConn(l[%s] r[%s])", localConn.LocalAddr().String(),
		localConn.RemoteAddr().String(), workConn.LocalAddr().String(), workConn.RemoteAddr().String())

	if connInfo.ProxyProtocolHeader != nil {
		if _, err := connInfo.ProxyProtocolHeader.WriteTo(localConn); err != nil {
			workConn.Close()
			xl.Errorf("write proxy protocol header to local conn error: %v", err)
			return
		}
	}

	_, _, errs := libio.Join(localConn, remote)
	xl.Debugf("join connections closed")
	if len(errs) > 0 {
		xl.Tracef("join connections errors: %v", errs)
	}
	if compressionResourceRecycleFn != nil {
		compressionResourceRecycleFn()
	}
}
