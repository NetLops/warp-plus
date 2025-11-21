# SOCKS5 代理池功能

## 快速开始

### 启用代理池

1. **复制示例配置**
   ```bash
   cp example_pool_config.json my_pool_config.json
   ```

2. **编辑配置文件**
   ```json
   {
     "proxy_pool": {
       "enabled": true,
       "strategy": "round-robin",
       "proxies": [
         {"bind": "127.0.0.1:8087", "endpoint": "", "weight": 1},
         {"bind": "127.0.0.1:8088", "endpoint": "", "weight": 1}
       ]
     }
   }
   ```

3. **启动代理池**
   ```bash
   ./warp-plus --config my_pool_config.json
   ```

## 负载均衡策略

| 策略 | 说明 | 适用场景 |
|------|------|----------|
| `round-robin` | 轮询分配 | 默认，适合均匀分布 |
| `least-connections` | 最少连接优先 | 连接时长不均匀 |
| `random` | 随机选择 | 简单场景 |
| `weighted-round-robin` | 加权轮询 | 代理性能差异大 |

## 配置参数

### 代理池全局配置

```json
{
  "enabled": true,                          // 启用代理池
  "strategy": "round-robin",                // 负载均衡策略
  "health_check_interval": "30s",           // 健康检查间隔
  "health_check_timeout": "5s",             // 健康检查超时
  "max_connections_per_proxy": 1000,        // 全局默认最大连接数
  "auto_recover": true                      // 自动恢复不健康代理
}
```

### 单个代理配置

```json
{
  "bind": "127.0.0.1:8087",        // 代理监听地址
  "endpoint": "162.159.192.1:500", // WireGuard 端点（可选）
  "weight": 1,                     // 权重（用于加权轮询）
  "max_connections": 1000          // 此代理最大连接数
}
```

## 监控

代理池会每 60 秒输出统计信息：

```
INFO proxy pool stats 
  total_proxies=3 
  healthy_proxies=3 
  active_connections=150 
  total_connections=10000 
  total_errors=5 
  error_rate=0.05%
```

## 海量 IP 支持

代理池支持海量 IP 代理：

- ✅ 每个代理使用独立的 WireGuard 隧道
- ✅ 并发安全的负载均衡
- ✅ 异步健康检查
- ✅ 自动故障转移

### 性能建议

- **1-10 个代理**: `round-robin` 或 `random`
- **10-50 个代理**: `least-connections`  
- **50+ 个代理**: `weighted-round-robin` + 根据性能设置权重

## 向后兼容

默认情况下代理池是禁用的，不会影响现有的单代理模式：

```bash
# 单代理模式（原有方式）
./warp-plus --bind 127.0.0.1:8086

# 代理池模式（新功能）
./warp-plus --config pool_config.json
```

## 示例：配置 10 个代理

```json
{
  "proxy_pool": {
    "enabled": true,
    "strategy": "least-connections",
    "health_check_interval": "30s",
    "proxies": [
      {"bind": "127.0.0.1:8087", "endpoint": "162.159.192.1:500"},
      {"bind": "127.0.0.1:8088", "endpoint": "162.159.192.2:500"},
      {"bind": "127.0.0.1:8089", "endpoint": "162.159.192.3:500"},
      {"bind": "127.0.0.1:8090", "endpoint": "162.159.192.4:500"},
      {"bind": "127.0.0.1:8091", "endpoint": "162.159.192.5:500"},
      {"bind": "127.0.0.1:8092", "endpoint": "162.159.192.6:500"},
      {"bind": "127.0.0.1:8093", "endpoint": "162.159.192.7:500"},
      {"bind": "127.0.0.1:8094", "endpoint": "162.159.192.8:500"},
      {"bind": "127.0.0.1:8095", "endpoint": "162.159.192.9:500"},
      {"bind": "127.0.0.1:8096", "endpoint": "162.159.192.10:500"}
    ]
  }
}
```

## 测试

```bash
# 测试单个代理
curl -x socks5://127.0.0.1:8087 https://www.google.com

# 并发测试（查看负载均衡效果）
for i in {1..100}; do
  curl -x socks5://127.0.0.1:8087 https://ifconfig.me &
done
wait
```

## 架构

```
Client → ProxyPool → LoadBalancer → Select ProxyInstance
                                           ↓
                                    WireGuard Tunnel
                                           ↓
                                    SOCKS5 Server
                                           ↓
                                        Internet
```

每个代理实例都有独立的 WireGuard 隧道和 SOCKS5 服务器。

## 技术特性

- **并发安全**: 使用 `sync.RWMutex` 和 `atomic` 操作
- **健康检查**: 定期检测并自动故障转移
- **统计监控**: 实时连接数、错误率等指标
- **灵活配置**: JSON 配置文件，易于管理
