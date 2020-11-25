# Formica CI

This CI program is a response to Jenkins and its fragile/bloated/outdated/difficult-to-maintain plugin ecosystem.

I wrote this based on the assumption that CI at its core, should be about orchestration of jobs in a simple and configurable way.
I have tried to make everything as scriptable as possible. You should be able to write the different parts of the system in a
language that suits your use case and experience, and share it as needed, without needing to access the core bits.

There is also a bias towards configuration-as-code, and everything is expected to be kept in some form of source control. I have even kept this bit scriptable, so that you can use any SCM you like.

## Initialization

The initialization logic consists firstly of a search for a directory/folder called `formica_conf` in the current directory (where the `formica` program is launched).

If it is not found, it will look for a script that starts with `config_init` (e.g. `config_init.sh`, `config_init.py`, or even just `config_init`). **There should be only one script with this prefix! The existence of two or more will cause an error.** This script should normally be a `git clone` command of sorts, that will download your job configuration from a Git repo. Therefore, the only thing you should need to setup/provision a new Formica orchestrator node is this file.

## The `formica_conf` folder
The repository should contain a script starting with `update` at the root. This should contain `git pull` or the equivalent for whatever SCM you are using, to update the jobs configuration to the latest version. This script will be invoked every 5 minutes by default, but you can change this (TODO).
