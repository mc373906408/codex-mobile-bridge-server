\set ON_ERROR_STOP on
\set bridge_password `cat /run/secrets/database_password`
CREATE ROLE bridge WITH LOGIN PASSWORD :'bridge_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE DATABASE bridge OWNER bridge;
