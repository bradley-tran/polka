<?php
define( 'DB_NAME', 'golden_db' );
define( 'DB_USER', 'golden_user' );
define( 'DB_PASSWORD', 'pa\'ss"w$rd\\#%20' );
define( 'DB_HOST', '127.0.0.1' );
define( 'DB_CHARSET', 'utf8mb4' );
define( 'DB_COLLATE', '' );

$table_prefix = 'wp_';

if ( ! defined( 'ABSPATH' ) ) {
  define( 'ABSPATH', __DIR__ . '/' );
}

require_once ABSPATH . 'wp-settings.php';
