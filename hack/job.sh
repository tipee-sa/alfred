#!/bin/sh

#mysql -u root -proot -h mysql mysql <<< 'SHOW DATABASES;'
# ^- binary             ^- host  ^- database

echo "Hello, $ALFRED_TASK" > $ALFRED_OUTPUT/hello.txt