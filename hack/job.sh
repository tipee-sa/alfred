#!/bin/sh

mysql -u root -proot -h mysql mysql <<< 'SHOW DATABASES;'
# ^- binary             ^- host  ^- database
