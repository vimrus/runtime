<?php
header('Content-Type: application/json');
echo json_encode(array(
    'url'   => getenv('ZENTAO_OBSERVABILITY_URL') ?: '',
    'token' => getenv('ZENTAO_OBSERVABILITY_TOKEN') ?: '',
    'query' => getenv('ZENTAO_OBSERVABILITY_QUERY_URL') ?: '',
    'mode'  => getenv('ZENTAO_LOG_MODE') ?: '',
));
