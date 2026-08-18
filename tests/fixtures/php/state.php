<?php

$GLOBALS['zentaoPocRequestCounter'] = ($GLOBALS['zentaoPocRequestCounter'] ?? 0) + 1;
header('Content-Type: application/json');
echo json_encode(array('counter' => $GLOBALS['zentaoPocRequestCounter']));
