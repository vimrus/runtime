<?php

header('Content-Type: application/json');
echo json_encode(array(
    'scriptName' => $_SERVER['SCRIPT_NAME'] ?? null,
    'pathInfo' => $_SERVER['PATH_INFO'] ?? null,
));
