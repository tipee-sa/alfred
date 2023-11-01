<?php
$replacements = [
    ',' => ' ',
    'server error=<nil>' => 'server',
    '[00] time=' => '',
    'time=' => '',
    '+01:00 level=' => ',',
    'Z level=' => ',',
    ' msg="' => ',',
    ' component=server ' => ',server,',
    ' component=server' => ',server,',
    ' component=provisioner ' => ',provisioner,',
    ' component=provisioner' => ',provisioner,',
    ' component=scheduler ' => ',scheduler,',
    ' component=scheduler' => ',scheduler,',
    ',scheduler,component=node ' => ',node,',
    ',scheduler,component=node' => ',node,',
    'intent="' => '',
    '"' => '',
    '\\' => '',
];
$content = file_get_contents(__DIR__ . '/../var/log.txt');
$content = str_replace(array_keys($replacements), array_values($replacements), $content);
$content = preg_replace('/task\.job=[a-z][a-z0-9_-]+ task\./', '', $content);
file_put_contents(__DIR__ . '/../var/log.csv', 'time,level,msg,component,payload' . PHP_EOL . trim($content, PHP_EOL) . PHP_EOL);
