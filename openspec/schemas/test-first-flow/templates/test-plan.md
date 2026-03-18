
## StepQueryByDBAlgor 查询行为测试
...

### 缓存交互测试
TC-16 缓存获取出错时从数据库获取数据  
说明：当缓存 Get 方法返回错误时，应降级到数据库获取数据，不影响正常查询流程  
GIVEN:
    mock cache.CacheReady 返回 true
    mock cache.Get 返回 error（如 redis 连接错误 "redis: connection refused"）
    mock cache.Set 返回 error（如 redis 连接错误 "redis: connection refused"）
    mock dao.GetLatestTime 返回 1704153600000
    mock dao.GetEarliestTime 返回 1704067200000
    mock dao.Query 返回包含 3 条记录的数据列表
WHEN:
    调用 FetchData(ctx, deviceProfile, queryParams)
    其中：
        queryParams.StartTime = 1704067200 (2024-01-01 00:00:00 UTC)
        queryParams.EndTime = 1704153600 (2024-01-02 00:00:00 UTC)
THEN:
    断言 error 为 nil（缓存错误不应导致查询失败）
    断言 返回的数据与 mock 的测试数据一致
