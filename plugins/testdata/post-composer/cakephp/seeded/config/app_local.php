<?php
return [
    'debug' => true,
    'Datasources' => [
        'default' => [
            'host' => '127.0.0.1',
            'username' => 'golden_user',
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Mysql',
            'port' => '3307',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8mb4',
            'url' => null,
        ],
        'test' => [
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Mysql',
            'host' => '127.0.0.1',
            'port' => '3307',
            'username' => 'golden_user',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8mb4',
            'url' => null,
        ],
        'debug_kit' => [
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Mysql',
            'host' => '127.0.0.1',
            'port' => '3307',
            'username' => 'golden_user',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8mb4',
            'url' => null,
        ],
    ],
];
