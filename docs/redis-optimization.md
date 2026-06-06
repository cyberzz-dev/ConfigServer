# Redis Agent Status Key 优化方案

## 已实施的修复

### 1. 修复 Key 分隔符问题 ✅
**问题**: `\x00` (NULL 字节) 作为分隔符在 Redis key 中显示异常

**解决方案**: 将分隔符从 `\x00` 改为 `:` (Redis 最佳实践)

**改动前后对比**:
```
改动前: agent_status:instance_id\x00config_name\x00pipeline
改动后: agent_status:instance_id:config_name:pipeline
```

### 2. 增强 Redis 清理机制 ✅
**问题**: Redis 中的 agent_status keys 仅依赖 7 天 TTL 被动清理，导致大量已停止 agent 的 keys 积累

**解决方案**: 在 `evictStaleAgents()` 中增加主动清理 Redis keys 的逻辑
- 每 5 分钟运行一次
- 清理超过 30 分钟未心跳的 agent 及其所有 status keys
- 日志记录清理的 agent 数量和 Redis keys 数量

**效果**:
- 原来: Redis 中的 keys 保留 7 天
- 现在: 30 分钟后主动清理，7 天 TTL 作为兜底

## 进一步优化方案（可选）

### 方案 A: 使用 Redis Hash 优化存储

**当前结构** (String keys):
```
agent_status:agent1:config1:pipeline → JSON
agent_status:agent1:config2:pipeline → JSON
agent_status:agent1:config3:instance → JSON
```
- 优点: 简单直观
- 缺点: key 数量 = agent 数 × config 数 (例如: 10万 × 10 = 100万 keys)

**优化后结构** (Hash):
```
agent_status:agent1 → Hash {
  "config1:pipeline": JSON,
  "config2:pipeline": JSON,
  "config3:instance": JSON
}
```
- 优点: key 数量 = agent 数 (例如: 10万 keys)，减少 90%
- 优点: 清理时一次 DEL 删除一个 agent 的所有 status
- 缺点: 需要重构代码

**实施步骤**:
1. 修改 `UpsertAgentConfigStatus` 使用 `HSET agent_status:{instanceID} {configName}:{configType} {JSON}`
2. 修改 `GetAgentConfigStatuses` 使用 `HGETALL agent_status:{instanceID}`
3. 修改 `listAgentConfigStatusesFromRedis` 使用 `SCAN` + `HGETALL`
4. 修改 `evictStaleAgents` 使用 `DEL agent_status:{instanceID}`

### 方案 B: 调整 TTL 策略

**当前策略**:
- 内存清理: 30 分钟 (agentTTL)
- Redis TTL: 7 天 (agentRedisTTL)

**建议调整** (根据实际业务场景):
```go
// 场景 1: Agent 重启频繁，需要保留历史状态
agentTTL     = 1 * time.Hour      // 1小时内存保留
agentRedisTTL = 24 * time.Hour     // 1天 Redis 保留

// 场景 2: Agent 稳定运行，快速释放资源
agentTTL     = 30 * time.Minute    // 30分钟内存保留 (当前值)
agentRedisTTL = 2 * time.Hour      // 2小时 Redis 保留

// 场景 3: 需要长期审计跟踪
agentTTL     = 30 * time.Minute    // 30分钟内存保留
agentRedisTTL = 7 * 24 * time.Hour // 7天 Redis 保留 (当前值)
// 但应将历史数据归档到 DB 而非 Redis
```

### 方案 C: 添加监控指标

在 `internal/metrics/collector.go` 中添加:

```go
// Redis keys 数量监控
RedisStatusKeysCount := prometheus.NewGauge(prometheus.GaugeOpts{
    Name: "configserver_redis_status_keys_total",
    Help: "Total number of agent status keys in Redis",
})

// 定期采集
func (c *Collector) collectRedisMetrics(ctx context.Context) {
    if c.rdb == nil {
        return
    }
    
    var cursor uint64
    var count int
    pattern := "agent_status:*"
    for {
        keys, nextCursor, err := c.rdb.Scan(ctx, cursor, pattern, 1000).Result()
        if err != nil {
            break
        }
        count += len(keys)
        cursor = nextCursor
        if cursor == 0 {
            break
        }
    }
    
    RedisStatusKeysCount.Set(float64(count))
}
```

### 方案 D: 批量删除优化

当前实现使用 `SCAN` + 多次 `DEL`，可以优化为使用 Pipeline:

```go
// 在 evictStaleAgents 中
pipe := m.rdb.Pipeline()
for _, id := range stale {
    pattern := prefixStatus + id + ":*"
    // 先 SCAN 收集所有 keys
    var allKeys []string
    var cursor uint64
    for {
        keys, nextCursor, err := m.rdb.Scan(ctx, cursor, pattern, 100).Result()
        if err != nil {
            break
        }
        allKeys = append(allKeys, keys...)
        cursor = nextCursor
        if cursor == 0 {
            break
        }
    }
    
    // 批量删除
    if len(allKeys) > 0 {
        pipe.Del(ctx, allKeys...)
    }
}
_, err := pipe.Exec(ctx)
```

## 推荐实施顺序

1. ✅ **已完成**: 修复 `\x00` 分隔符问题
2. ✅ **已完成**: 增强 Redis 主动清理机制
3. **短期**: 添加监控指标（方案 C），观察实际 key 数量和增长趋势
4. **中期**: 根据监控数据调整 TTL 策略（方案 B）
5. **长期**: 如果 key 数量仍然是瓶颈，实施 Hash 结构重构（方案 A）

## 兼容性说明

**重要**: 修改分隔符后，旧的 Redis keys (使用 `\x00`) 不会自动清理

**迁移方案**:
```bash
# 方式 1: 等待 7 天 TTL 自然过期
# 适合: 非生产环境或 key 数量较少

# 方式 2: 手动清理旧 keys
redis-cli --scan --pattern "agent_status:*" | while read key; do
    # 检查 key 中是否包含 \x00
    if redis-cli --raw GET "$key" | grep -q '\x00'; then
        redis-cli DEL "$key"
    fi
done

# 方式 3: 设置短 TTL 加速过期
redis-cli --scan --pattern "agent_status:*" | xargs -L 100 redis-cli EXPIRE 3600
# 将所有 agent_status keys 的 TTL 设置为 1 小时
```

## 性能影响评估

**内存占用**:
- 每个 status key: ~250 字节
- 100 万 keys: ~250 MB
- Hash 优化后: ~100 MB (减少 60%)

**清理性能**:
- SCAN 速度: ~10,000 keys/秒
- 清理 10 万个过期 agent 的 100 万 keys: ~100 秒
- 建议: 在低峰期运行，或分批清理

**网络开销**:
- 当前每次心跳: 1 次 `HSET` (agent) + N 次 `SET` (status)
- Hash 优化后: 1 次 `HSET` (agent) + 1 次 `HMSET` (所有 status)
- 减少网络往返次数
