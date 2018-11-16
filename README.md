# What is roller?
Dice roller written in Go and Javascript to run on Google Appengine

You can try it out here: https://rollforyour.party

Modeled after http://catchyourhare.com/diceroller/ with a few additions and changes.

## Starting roller using Docker

 1. Install [Docker](https://docs.docker.com/install/)
 2. In this directory, run `bin/launch_in_docker.sh`

This will build an image with all dependencies installed, and then run
the server on port 8080.
