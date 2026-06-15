# scenarios/sandbox-cluster.hcl — a minimal cluster.hcl pointed at
# a throwaway sandbox cluster. Suitable for the example.hcl scenario.
#
#   weft-chaos --cluster scenarios/sandbox-cluster.hcl \
#              --scenario scenarios/example.hcl
#
# Real cluster.hcl in the weft repo carries dc / hypervisor / driver
# blocks — chaos ignores them, only the three top-level attrs
# matter to the harness.

name       = "sandbox-lab"
production = false
portal_url = "https://sandbox.weft.example.com"
