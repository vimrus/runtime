<?php
$url   = getenv('ZENTAO_OBSERVABILITY_URL');
$token = getenv('ZENTAO_OBSERVABILITY_TOKEN');
$node  = getenv('ZENTAO_NODE_ID');
$boot  = getenv('ZENTAO_BOOT_ID');
if(!$url || !$token || !$node || !$boot)
{
    http_response_code(503);
    echo '{"error":{"code":"unavailable","message":"observability environment missing"}}';
    exit;
}

$payload = json_encode(array(
    'schemaVersion' => 1,
    'events' => array(array(
        'clusterID' => 'cluster-smoke',
        'nodeID'    => $node,
        'bootID'    => $boot,
        'eventID'   => 'e2e-' . bin2hex(random_bytes(4)),
        'source'    => 'php',
        'kind'      => 'logs',
        'level'     => 'info',
        'message'   => 'observability bridge smoke',
    ), array(
        'clusterID'  => 'cluster-smoke',
        'nodeID'     => $node,
        'bootID'     => $boot,
        'eventID'    => 'e2e-metric-' . bin2hex(random_bytes(4)),
        'source'     => 'php',
        'kind'       => 'metrics',
        'metricName' => 'observability_events_received_total',
        'metricKind' => 'counter',
        'value'      => 1,
        'labels'     => array('kind' => 'logs', 'source' => 'php'),
    )),
), JSON_UNESCAPED_UNICODE);

$context = stream_context_create(array('http' => array(
    'method'        => 'POST',
    'header'        => "Content-Type: application/json\r\n"
        . "X-Zentao-Observability-Token: {$token}\r\n"
        . "X-Zentao-Observability-Schema: 1",
    'content'       => $payload,
    'timeout'       => 1,
    'ignore_errors' => true,
)));
$response = @file_get_contents($url, false, $context);
header('Content-Type: application/json');
echo $response === false ? '{"accepted":0}' : $response;
