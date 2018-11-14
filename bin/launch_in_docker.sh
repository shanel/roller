#! /bin/bash

echo ""
echo "Building the Docker image..."
echo ""
docker build . -t roller

# this command would start a shell:
# docker run --rm -p 8080:8080 -v $PWD/project:/gocode/app/project -it roller bash

echo ""
echo "Starting the server on port 8080..."
echo ""
docker run --rm -p 8080:8080 -v $PWD/project:/gocode/app/project -it roller
