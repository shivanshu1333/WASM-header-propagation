// Copyright 2020-2021 Tetrate
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"github.com/tidwall/gjson"
	"regexp"
	"strconv"
	"strings"

	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
)

var (
	// outboundClusterPattern is a pattern that matches the format of an Istio outbound
	// cluster, to identify egress requests. The capture group captures the 'subset'.
	outboundClusterPattern = regexp.MustCompile(`^outbound\|([0-9]+)\|(.*)\|(.*)$`)
	inboundClusterPattern  = regexp.MustCompile(`^inbound\|([0-9]+)\|(.*)\|(.*)$`)
)

const (
	SwimLaneHeader = "x-swimlane-header"
	ServiceIndex   = "x-swimlane-service-index"
	ServiceSubset  = "x-swimlane-service-subset"
)

func main() {
	proxywasm.SetVMContext(&vmContext{})
}

type vmContext struct {
	// Embed the default VM context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultVMContext
}

// NewPluginContext Override types.DefaultVMContext.
func (*vmContext) NewPluginContext(contextID uint32) types.PluginContext {
	return &pluginContext{}
}

type pluginContext struct {
	// Embed the default plugin context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultPluginContext
	swimLanes map[string]map[int][]string
}

type routeInfo struct {
	// Embed the default http context here,
	// so that we don't need to reimplement all the methods.
	types.DefaultHttpContext
	contextID    uint32
	headerValue  string
	serviceIndex int
	swimLanes    map[string]map[int][]string
}

// NewHttpContext Override types.DefaultPluginContext.
func (p *pluginContext) NewHttpContext(contextID uint32) types.HttpContext {
	return &routeInfo{
		contextID: contextID,
		swimLanes: p.swimLanes,
	}
}

func (p *pluginContext) OnPluginStart(pluginConfigurationSize int) types.OnPluginStartStatus {
	proxywasm.LogDebug("loading plugin config")
	data, err := proxywasm.GetPluginConfiguration()
	if data == nil {
		return types.OnPluginStartStatusOK
	}

	if err != nil {
		proxywasm.LogCriticalf("error reading plugin configuration: %v", err)
		return types.OnPluginStartStatusFailed
	}
	config := string(data)
	if !gjson.Valid(config) {
		proxywasm.LogCritical(`invalid configuration format; expected {"header": "<header name>", "value": "<header value>"}`)
		return types.OnPluginStartStatusFailed
	}
	swimLanes := gjson.Get(config, "swimLanes").Map()

	for key, value := range swimLanes {
		for index, serviceSubset := range value.Array() {
			p.swimLanes[key][index] = strings.Split(serviceSubset.String(), ",")
		}
	}

	return types.OnPluginStartStatusOK
}

// OnHttpRequestHeaders Override types.DefaultHttpContext.
func (ctx *routeInfo) OnHttpRequestHeaders(numHeaders int, endOfStream bool) types.Action {

	nodeId, err := proxywasm.GetProperty([]string{"node", "id"})
	if err != nil {
		proxywasm.LogWarnf("error reading property 'node.id': %v", err)
	}
	if strings.HasPrefix(string(nodeId), "router") {
		proxywasm.LogDebug("omitting version parsing for gateways")
		return types.ActionContinue
	}

	cluster, err := proxywasm.GetProperty([]string{"cluster_name"})
	if err != nil {
		proxywasm.LogWarnf("error reading property 'cluster_name': %v", err)
	}
	proxywasm.LogInfof("cluster %s", cluster)

	routenname, err := proxywasm.GetProperty([]string{"route_name"})
	if err != nil {
		proxywasm.LogWarnf("error reading property 'router_name': %v", err)
	}
	proxywasm.LogInfof("plugin name %s", routenname)
	if err != nil {
		proxywasm.LogWarnf("error getting headers: %v", err)
	}
	hs, err := proxywasm.GetHttpRequestHeaders()

	if inboundClusterPattern.Match(cluster) {
		serviceIndexFound := false
		for _, h := range hs {
			if h[0] == SwimLaneHeader {
				ctx.headerValue = h[1]
				proxywasm.LogInfof("inbound header x-swimlane %s", ctx.headerValue)
			}
			if h[0] == ServiceIndex {
				serviceIndexFound = true
				ctx.serviceIndex, _ = strconv.Atoi(h[1])
				proxywasm.LogInfof("inbound header x-swimlane-service-index %d", ctx.serviceIndex)
			}
		}
		if !serviceIndexFound {
			ctx.serviceIndex = 0
			proxywasm.LogInfof("inbound header x-swimlane-service-index %d", ctx.serviceIndex)
		}
	} else {
		err = proxywasm.AddHttpRequestHeader(SwimLaneHeader, ctx.headerValue)
		if err != nil {
			proxywasm.LogCriticalf("failed to set request headers: %v", err)
		}
		proxywasm.LogInfof("added outbound header x-swimlane %s", ctx.headerValue)
		var outboundSvc string
		for _, h := range hs {
			if h[0] == ":path" {
				outboundSvc = strings.Split(h[1], "/")[0]
			}
			proxywasm.LogInfof("outbound svc %s", outboundSvc)
		}
		ctx.serviceIndex++
		services := ctx.swimLanes[ctx.headerValue][ctx.serviceIndex]
	out:
		for _, serviceList := range services {
			for _, outboundService := range strings.Split(serviceList, ",") {
				if strings.HasPrefix(outboundSvc, strings.Split(outboundService, "-")[0]) {
					proxywasm.LogInfof("outboundSvc %s", strings.Split(outboundService, "-")[0])
					err = proxywasm.AddHttpRequestHeader(ServiceSubset, strings.Split(outboundService, "-")[1])
					if err != nil {
						proxywasm.LogCriticalf("failed to set request headers: %v", err)
					}
					proxywasm.LogInfof("added outbound header x-swimlane-service-subset %s", strings.Split(outboundService, "-")[1])
					break out
				}
			}
		}

	}

	finalHeaders, err := proxywasm.GetHttpRequestHeaders()
	if err != nil {
		proxywasm.LogWarnf("error getting headers: %v", err)
	}
	for _, h := range finalHeaders {
		proxywasm.LogInfof("request header --> %s: %s", h[0], h[1])
	}

	return types.ActionContinue
}

// OnHttpStreamDone Override types.DefaultHttpContext.
func (ctx *routeInfo) OnHttpStreamDone() {
	proxywasm.LogInfof("%d finished", ctx.contextID)
}
