#!/bin/sh

#mysql -u root -proot -h mysql mysql <<< 'SHOW DATABASES;'
# ^- binary             ^- host  ^- database

sleep 10
echo "Hello, $ALFRED_TASK" > $ALFRED_OUTPUT/hello.txt