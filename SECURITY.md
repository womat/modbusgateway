# Security

modbusgateway runs on a Raspberry Pi in a home network and gives clients read and write access to
Modbus devices - possibly a heat pump, an inverter or other equipment that controls mains power -
through an HTTPS API and Modbus listeners. Reports of security issues are taken seriously.

Modbus itself has no authentication: anyone who can reach a Modbus listener can use every function
code the configured devices allow. That is by design and documented in the README; bind the listener
to a trusted interface or protect it with a firewall.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub instead:
**Security → Report a vulnerability** ([direct link](https://github.com/womat/modbusgateway/security/advisories/new)).

Helpful details:

- the affected version (`modbusgateway --version`, or `GET /version` on the device)
- steps to reproduce
- what an attacker could achieve with it

You will usually get an answer within a week. modbusgateway is a spare-time project, so no fixed
response time can be promised.

## Supported versions

Security fixes are made for the latest release only.
