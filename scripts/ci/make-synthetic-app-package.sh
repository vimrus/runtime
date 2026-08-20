#!/usr/bin/env bash
#
# Create a small synthetic ZenTao application package for CI pipeline
# validation. Real packages are provided externally; this is only used to
# exercise staging/generation on runners without paid sources.
#
# Usage: make-synthetic-app-package.sh <output-dir>

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: make-synthetic-app-package.sh <output-dir>" >&2
    exit 2
fi

readonly output="$1"
mkdir -p "${output}/www"
cat > "${output}/www/index.php" <<'EOF'
<?php
if(isset($_GET['fatal'])) trigger_error('synthetic fatal', E_USER_ERROR);
header('Content-Type: application/json');
echo json_encode(array('php' => PHP_VERSION, 'zts' => (bool) PHP_ZTS, 'ioncube' => extension_loaded('ionCube Loader')));
EOF
cat > "${output}/www/install.php" <<'EOF'
<?php echo 'synthetic install'; ?>
EOF
printf '22.5\n' > "${output}/VERSION"
echo "synthetic application package: ${output}"
