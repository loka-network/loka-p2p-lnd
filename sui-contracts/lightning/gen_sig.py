import hashlib
import ecdsa

# LND uses secp256k1
sk = ecdsa.SigningKey.generate(curve=ecdsa.SECP256k1, hashfunc=hashlib.sha256)
vk = sk.get_verifying_key()
pubkey = vk.to_string("compressed")

# Example message to sign for force_close
# Let's say channel_id is 32 bytes of 0x11
channel_id = b'\x11' * 32
state_num = (5).to_bytes(8, byteorder='little') # u64 little endian
msg = channel_id + state_num

# secp256k1 over sha256
signature = sk.sign(msg, hashfunc=hashlib.sha256, sigencode=ecdsa.util.sigencode_string)

print("pubkey:", pubkey.hex())
print("msg:", msg.hex())
print("signature:", signature.hex())

# For penalize
revocation_secret = b'\x22' * 32
signature_penalize = sk.sign(revocation_secret, hashfunc=hashlib.sha256, sigencode=ecdsa.util.sigencode_string)
print("sig_penalize:", signature_penalize.hex())
