<?php
/**
 * Redis 共享后端 smoke fixture。
 *
 * 由 redis-cache-smoke.sh 在 runtime 镜像内运行，验证 phpredis 扩展与
 * redisCacheBackend/cacheService 的真实 Redis 读写与 namespace 版本失效。
 */
$serviceRoot = getenv('ZENTAO_SERVICE_ROOT') ?: dirname(__DIR__, 2) . '/service';
require_once $serviceRoot . '/cache/cache.service.interface.php';
require_once $serviceRoot . '/cache/cache.backend.interface.php';
require_once $serviceRoot . '/cache/cache.service.php';
require_once $serviceRoot . '/cache/backend/redis.backend.php';

$host = getenv('ZT_REDIS_HOST') ?: '127.0.0.1';
$port = (int)(getenv('ZT_REDIS_PORT') ?: 6379);

$redis = new Redis();
if(!$redis->connect($host, $port, 1)) exit('redis connect failed');
if(!$redis->ping()) exit('redis ping failed');

$backend = new redisCacheBackend($redis);
$cache   = new cacheService($backend);

$result = array(
    'set'    => $cache->set('smoke', 'redis', array('a' => 1, 'b' => 'text'), 60),
    'get'    => $cache->get('smoke', 'redis'),
    'miss'   => $cache->get('smoke', 'missing-key'),
);

/* 第二个实例直接读同一个 Redis key，验证真实共享语义。 */
$backend2 = new redisCacheBackend($redis);
$cache2   = new cacheService($backend2);
$result['shared_get'] = $cache2->get('smoke', 'redis');

$cache->clear('smoke');
$backend3 = new redisCacheBackend($redis);
$cache3   = new cacheService($backend3);
$result['after_clear'] = $cache3->get('smoke', 'redis');

$result['delete'] = $cache3->delete('smoke', 'after-clear-write');
$result['re_set'] = $cache3->set('smoke', 'after-clear-write', 'ok', 60);
$result['re_get'] = $cache3->get('smoke', 'after-clear-write');
$result['final_delete'] = $cache3->delete('smoke', 'after-clear-write');

header('Content-Type: application/json');
echo json_encode($result, JSON_UNESCAPED_UNICODE);
