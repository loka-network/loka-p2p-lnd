#[test_only]
module lightning::test_hash {
    use sui::ecdsa_k1;
    use sui::hash;
    
    #[test]
    fun test_signature() {
        // Go output:
        // pubkey: 02e5a29e3f37847730f09c45cfb99ad95ef3e76bd5ac1bc80c653f395d296a686b
        // raw revSecret in Go: 2222222222222222222222222222222222222222222222222222222222222222
        // penalize sig: 9c3de75cc8d3941b656f4faaa5c01c636f0c939207a5fbf77412fcee9886aaa4691484ab7a3f144129317cfc027229b84dfc20b2961094ff2f8ffc4e88d00ab6

        let pubkey = x"02e5a29e3f37847730f09c45cfb99ad95ef3e76bd5ac1bc80c653f395d296a686b";
        let msg = x"2222222222222222222222222222222222222222222222222222222222222222";
        let sig = x"9c3de75cc8d3941b656f4faaa5c01c636f0c939207a5fbf77412fcee9886aaa4691484ab7a3f144129317cfc027229b84dfc20b2961094ff2f8ffc4e88d00ab6";

        // test with hash=1 (SHA256)
        assert!(ecdsa_k1::secp256k1_verify(&sig, &pubkey, &msg, 1), 0);
    }
}
