---
title: Storage Provider Command Line Interface
---

There is no set way to create a CLI, but Moca Storage Provider typically use the [urfave cli library v2](https://github.com/urfave/cli).
Building a CLI with urfave cli entails defining commands, arguments, and flags. Commands understand the actions users wish to take, such as `config.dump` cmd dumps default configuration for editing and `config.upload` uploads the configuration to db, all service gets the configuration from db with `--config.remote` flag.
Users also supply **Arguments**, such as the db address that uploading configuration and flags to modify various aspects of the commands, such `--server gateway, uploader, downaloader` starts the gateway, uploader and downloader services in one process, combines the required services arbitrarily.

## Commands

```shell
$ ./moca-sp version

Moca Storage Provider
    __                                                       _     __
    _____/ /_____  _________ _____ ____     ____  _________ _   __(_)___/ /__  _____
    / ___/ __/ __ \/ ___/ __  / __  / _ \   / __ \/ ___/ __ \ | / / / __  / _ \/ ___/
    (__  ) /_/ /_/ / /  / /_/ / /_/ /  __/  / /_/ / /  / /_/ / |/ / / /_/ /  __/ /
    /____/\__/\____/_/   \__,_/\__, /\___/  / .___/_/   \____/|___/_/\__,_/\___/_/
    /____/       /_/

Version : v1.0.0
Branch  : master
Commit  : 7e1f56809c5385bf1ea6f41d318ab1419dcb0f86
Build   : go1.20.3 darwin arm64 2023-10-08 10:31
```

```shell
$ ./moca-sp --help

NAME:
   moca-sp - the Moca Storage Provider command line interface

USAGE:
   moca-sp [global options] command [command options] [arguments...]

COMMANDS:
   help, h  Shows a list of commands or help for one command
   CONFIG COMMANDS:
     config.dump  Dump default configuration to the './config.toml' file for editing
   MIGRATE COMMANDS:
     sp.exit              Used for sp exits from the Moca storage network
     sp.complete.exit     Only used in debugging scenarios, online use not allowed. Used for sp complete exits from the Moca storage network.
     sp.complete.swapout  Only used in debugging scenarios, online use not allowed. Used for swap out from the Moca storage network.
   MISCELLANEOUS COMMANDS:
     version, v  Print version information
   QUERY COMMANDS:
     list.modules          List the modules in moca storage provider
     list.errors           List the predefine errors in moca storage provider
     query.task            Query running tasks in modules by task sub key
     get.object            Get object payload data
     challenge.piece       Challenge piece integrity hash
     get.piece.integrity   Get piece integrity hash and signature
     query.bucket.migrate  Query bucket migrate plan and status
   QUOTA COMMANDS:
     update.quota  Update the free quota of the SP
   RECOVERY COMMANDS:
     recover.object  Generate recover piece data tasks to recover the object data
     recover.piece   Generate recover piece data task to recover the object piece
```

## Global Options

### Config Commands

Dump default configuration to the `./config.toml` file for editing:

```shell
Example:
$ ./moca-sp config.dump
```

### Migrate Commands

:::note
SP exit and bucket migration functions are opened in `Testnet` and not opened in `Mainnet`. Therefore, `sp.exit`, `sp.complete.exit` and `sp.complete.swapout` can be used in `Testnet`. Don't use them in `Mainnet`.
:::

#### sp.exit

Using this command, it will send an transaction to Moca blockchain to tell this SP is prepared to exit from Moca storage network.

```shell
USAGE:
   moca-sp sp.exit [command options] [arguments...]

Example:
$ ./moca-sp sp.exit --operatorAddress ${operator_address}
```

#### sp.complete.exit

Using this command, it will send an transaction to Moca blockchain to tell this SP is prepared to complete exit from Moca storage network.

Only used in debugging scenarios, online use not allowed.

```shell
USAGE:
   moca-sp sp.complete.exit [command options] [arguments...]

Example:
$ ./moca-sp sp.complete.exit --operatorAddress {operator_address}
```

#### sp.complete.swapout

Using this command, it will send an transaction to Moca blockchain to tell this SP is prepared to swap out from Moca storage network.

Only used in debugging scenarios, online use not allowed.

```shell
USAGE:
   moca-sp sp.complete.swapout [command options] [arguments...]

$ ./moca-sp sp.complete.swapout --operatorAddress {openrator_address} --familyID 2 --gvgIDList '1,2,3'
```

### Query Commands

#### list.modules

List the modules in moca storage provider.

```shell
USAGE:
   moca-sp list.modules [command options] [arguments...]

Example:
$ ./moca-sp list.modules

# Output
approver             Handles the ask crate bucket/object and replicates piece approval request.
authenticator        Checks authentication.
blocksyncer          Synchronize data on the chain to SP
downloader           Downloads object and gets challenge info and statistical read traffic from the backend.
gateway              Receives the user request and routes to the responding service.
manager              Manages SPs and schedules tasks.
metadata             Retrieves sp metadata and info.
p2p                  Communicates between SPs on p2p protocol.
receiver             Receives data pieces of an object from other storage provider and store.
signer               Signs the transaction and broadcasts to chain.
taskexecutor         Executes background tasks.
uploader             Uploads object payload to primary SP.
```

#### list.errors

List the predefine errors in Moca storage provider.

```shell
USAGE:
   moca-sp list.errors [command options] [arguments...]

Example:
$ ./moca-sp list.errors

# Output
code_space:"approver" http_status_code:400 inner_code:10001 description:"OoooH.... request lost"
code_space:"approver" http_status_code:406 inner_code:10002 description:"account buckets exceed the limit"
code_space:"authenticator" http_status_code:400 inner_code:20001 description:"unsupported auth op type"
code_space:"authenticator" http_status_code:400 inner_code:20002 description:"mismatched primary sp"
......
```

#### get.object

The get.object command send rpc request to downloader server to get object payload data.

```shell
USAGE:
   moca-sp get.object [command options] [arguments...]

Example:
$ ./moca-sp get.object -i 123 --config ./config.toml
```

#### challenge.piece

The challenge.piece command send rpc request to downloader get integrity meta and check the piece checksums.

```shell
USAGE:
   moca-sp challenge.piece [command options] [arguments...]

Example:
$ ./moca-sp challenge.piece -i 123 -r 1 -s 2 --config ./config.toml
```

#### get.piece.integrity

The get.piece.integrity command send rpc request to spdb get integrity hash and signature.

```shell
USAGE:
   moca-sp get.piece.integrity [command options] [arguments...]

Example:
$ ./moca-sp get.piece.integrity -i 10 --config ./config.toml
```

#### query.bucket.migrate

The query.bucket.migrate command send rpc request to manager get plan and status.

```shell
USAGE:
   moca-sp query.bucket.migrate [command options] [arguments...]

Example:
$ ./moca-sp query.bucket.migrate --config ./config.toml
```

#### update.quota

The update.quota command is used to update the free quota of the SP on moca chain, it will send a txn to the chain to finish the updating.

```shell
USAGE:
   moca-sp update.quota [command options] [arguments...]

Example:
$ ./moca-sp update.quota --quota 100000 --config ./config.toml
```

#### recover.object

The recover.object command is used to recover the object  data on the primarySP or the secondary SP

```shell
USAGE:
   moca-sp recover.object [command options] [arguments...]

Example:
$ ./moca-sp recover.object -b testbucket -o testobject --config ./config.toml
```

#### recover.piece

The recover.piece command is used to recover the object piece data on the primarySP or the secondary SP.

```shell
USAGE:
   moca-sp recover.piece [command options] [arguments...]

Example:
$ ./moca-sp recover.object -b testbucket -o testobject -s 1 --config ./config.toml
```
