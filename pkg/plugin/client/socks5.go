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
	"io"
	"log"

	gosocks5 "github.com/armon/go-socks5"

	v1 "github.com/SianHH/frp-package/pkg/config/v1"
	netpkg "github.com/SianHH/frp-package/pkg/util/net"
)

func init() {
	Register(v1.PluginSocks5, NewSocks5Plugin)
}

type Socks5Plugin struct {
	Server *gosocks5.Server
}

func NewSocks5Plugin(_ PluginContext, options v1.ClientPluginOptions) (p Plugin, err error) {
	opts := options.(*v1.Socks5PluginOptions)

	cfg := &gosocks5.Config{
		Logger: log.New(io.Discard, "", log.LstdFlags),
	}
	if opts.Username != "" || opts.Password != "" {
		cfg.Credentials = gosocks5.StaticCredentials(map[string]string{opts.Username: opts.Password})
	}
	sp := &Socks5Plugin{}
	sp.Server, err = gosocks5.New(cfg)
	p = sp
	return
}

func (sp *Socks5Plugin) Handle(_ context.Context, connInfo *ConnectionInfo) {
	defer connInfo.Conn.Close()
	wrapConn := netpkg.WrapReadWriteCloserToConn(connInfo.Conn, connInfo.UnderlyingConn)
	_ = sp.Server.ServeConn(wrapConn)
}

func (sp *Socks5Plugin) Name() string {
	return v1.PluginSocks5
}

func (sp *Socks5Plugin) Close() error {
	return nil
}
