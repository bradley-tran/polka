<?php
declare(strict_types=1);

return [
    'Datasources' => [
        'default' => [
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Postgres',
            'host' => '127.0.0.1',
            'port' => '5433',
            'username' => 'golden_user',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8',
            'url' => null,
        ],
        'test' => [
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Postgres',
            'host' => '127.0.0.1',
            'port' => '5433',
            'username' => 'golden_user',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8',
            'url' => null,
        ],
        'debug_kit' => [
            'className' => 'Cake\\Database\\Connection',
            'driver' => 'Cake\\Database\\Driver\\Postgres',
            'host' => '127.0.0.1',
            'port' => '5433',
            'username' => 'golden_user',
            'password' => 'pa\'ss"w$rd\\#%20',
            'database' => 'golden_db',
            'encoding' => 'utf8',
            'url' => null,
        ],
    ],
];
