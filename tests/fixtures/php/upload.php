<?php

header('Content-Type: application/json');
$file = $_FILES['payload'] ?? null;
echo json_encode(array(
    'name' => $file['name'] ?? null,
    'size' => $file['size'] ?? null,
    'error' => $file['error'] ?? null,
));
