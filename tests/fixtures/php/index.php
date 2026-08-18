<?php

header('Content-Type: application/json');
echo json_encode(array(
    'php' => PHP_VERSION,
    'zts' => (bool) PHP_ZTS,
    'ioncube' => extension_loaded('ionCube Loader'),
));
