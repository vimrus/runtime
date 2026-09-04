<?php
require_once dirname(__DIR__) . '/service/runtime/runtime.service.php';

$cache = runtimeService::cache();
$namespace = 'smoke';
$key       = 'cache-bridge-smoke';

$set = $cache->set($namespace, $key, 'hello-cache', 60);
$get = $cache->get($namespace, $key);
$del = $cache->delete($namespace, $key);

header('Content-Type: application/json');
echo json_encode(array(
    'set'    => $set,
    'get'    => $get,
    'delete' => $del,
), JSON_UNESCAPED_UNICODE);
