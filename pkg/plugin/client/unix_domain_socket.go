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

//go:build !frps

package client

import (
	"context"
	"net"

	libio "github.com/fatedier/golib/io"

	v1 "github.com/SianHH/frp-package/pkg/config/v1"
	"github.com/SianHH/frp-package/pkg/util/xlog"
)

func init() {
	Register(v1.PluginUnixDomainSocket, NewUnixDomainSocketPlugin)
}

type UnixDomainSocketPlugin struct {
	UnixAddr *net.UnixAddr
}

func NewUnixDomainSocketPlugin(_ PluginContext, options v1.ClientPluginOptions) (p Plugin, err error) {
	opts := options.(*v1.UnixDomainSocketPluginOptions)

	unixAddr, errRet := net.ResolveUnixAddr("unix", opts.UnixPath)
	if errRet != nil {
		err = errRet
		return
	}

	p = &UnixDomainSocketPlugin{
		UnixAddr: unixAddr,
	}
	return
}

func (uds *UnixDomainSocketPlugin) Handle(ctx context.Context, connInfo *ConnectionInfo) {
	xl := xlog.FromContextSafe(ctx)
	localConn, err := net.DialUnix("unix", nil, uds.UnixAddr)
	if err != nil {
		xl.Warnf("dial to uds %s error: %v", uds.UnixAddr, err)
		return
	}
	if connInfo.ProxyProtocolHeader != nil {
		if _, err := connInfo.ProxyProtocolHeader.WriteTo(localConn); err != nil {
			return
		}
	}

	libio.Join(localConn, connInfo.Conn)
}

func (uds *UnixDomainSocketPlugin) Name() string {
	return v1.PluginUnixDomainSocket
}

func (uds *UnixDomainSocketPlugin) Close() error {
	return nil
}
