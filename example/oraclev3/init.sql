-- Runs once, as SYS, when the container creates its database.
-- The scott user itself is created by APP_USER in FREEPDB1.
ALTER SESSION SET CONTAINER = FREEPDB1;
CREATE TABLE scott.BONUS (ENAME VARCHAR2(10), JOB VARCHAR2(9), SAL NUMBER, COMM NUMBER);
