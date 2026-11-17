package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var blockedOutboundPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicOutboundAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedOutboundPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validateBarkURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 2048 {
		return errors.New("Bark URL 过长")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("Bark URL 必须是无账号信息和片段的 HTTPS 地址")
	}
	if address, err := netip.ParseAddr(parsed.Hostname()); err == nil && !isPublicOutboundAddress(address) {
		return errors.New("Bark URL 不允许使用内网或保留地址")
	}
	return nil
}

func buildBarkURL(endpoint, title, content string) (string, error) {
	if err := validateBarkURL(endpoint); err != nil {
		return "", err
	}
	if strings.TrimSpace(endpoint) == "" {
		return "", errors.New("尚未配置 Bark URL")
	}
	if strings.Contains(endpoint, "{{title}}") || strings.Contains(endpoint, "{{content}}") {
		result := strings.ReplaceAll(endpoint, "{{title}}", url.QueryEscape(title))
		return strings.ReplaceAll(result, "{{content}}", url.QueryEscape(content)), nil
	}
	return "", errors.New("Bark URL 模板缺少 {{title}} 或 {{content}}")
}

func buildBarkRequest(endpoint, title, content string) (*http.Request, error) {
	if err := validateBarkURL(endpoint); err != nil {
		return nil, err
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("尚未配置 Bark URL")
	}
	if strings.Contains(endpoint, "{{title}}") || strings.Contains(endpoint, "{{content}}") {
		finalURL, err := buildBarkURL(endpoint, title, content)
		if err != nil {
			return nil, err
		}
		return http.NewRequest(http.MethodGet, finalURL, nil)
	}
	parsed, _ := url.Parse(endpoint)
	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return nil, errors.New("Bark URL 缺少设备密钥")
	}
	segments := strings.Split(path, "/")
	deviceKey, err := url.PathUnescape(segments[len(segments)-1])
	if err != nil || strings.TrimSpace(deviceKey) == "" {
		return nil, errors.New("Bark URL 的设备密钥无效")
	}
	segments[len(segments)-1] = "push"
	parsed.Path = "/" + strings.Join(segments, "/")
	parsed.RawPath = ""
	payload, err := json.Marshal(map[string]string{
		"device_key": deviceKey,
		"title":      title,
		"body":       content,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request, nil
}

func sendBark(client *http.Client, endpoint, title, content string) error {
	request, err := buildBarkRequest(endpoint, title, content)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Zpika-Feedback/1.0")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("发送 Bark 通知失败，请检查通知地址和网络")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Bark 返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func notificationClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析 Bark 地址失败: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("解析 Bark 主机失败: %w", err)
		}
		for _, candidate := range addresses {
			if !isPublicOutboundAddress(candidate) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		}
		return nil, errors.New("Bark 主机未解析到可用的公网地址")
	}
	return &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("Bark 请求不允许重定向")
		},
	}
}
