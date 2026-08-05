-- Create the UNICA business database alongside Dify's own.
--
-- Sharing one PostgreSQL instance is a preview convenience: it halves the memory
-- footprint and keeps the whole stack in one compose project. Production keeps
-- them separate, since Dify's write load and UNICA's have nothing to do with
-- each other.
--
-- Runs only on first initialisation of an empty data directory.

CREATE DATABASE unica;

\connect unica

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS pgcrypto;
