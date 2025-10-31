#!/bin/bash

cd sweeper

KO_DOCKER_REPO=kind.local ko build --base-import-paths .

cd ..