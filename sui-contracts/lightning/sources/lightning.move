module lightning::lightning {
    use sui::coin::{Self, Coin};
    use sui::balance::{Self, Balance};
    use sui::sui::SUI;
    use sui::event;
    use sui::table::{Self, Table};
    use sui::ecdsa_k1;
    use sui::clock::{Self, Clock};
    use sui::bcs;
    use std::hash;

    // --- Errors ---
    const EInvalidSignature: u64 = 0;
    const EInvalidStateNum: u64 = 1;
    const EChannelNotOpen: u64 = 2;
    #[allow(unused_const)]
    const EInsufficientBalance: u64 = 3;
    const EInvalidPreimage: u64 = 4;
    const ENotExpired: u64 = 5;
    const EInvalidHash: u64 = 6;
    const EInvalidStatus: u64 = 7;
    const EInvalidLength: u64 = 8;
    const EDelayTooShort: u64 = 10;

    const MIN_TO_SELF_DELAY_MS: u64 = 86_400_000; // 24 Hours Default

    // --- Data Structures ---

    public struct Channel has key {
        id: UID,
        party_a: address,
        party_b: address,
        balance_a: u64,
        balance_b: u64,
        funding_balance: Balance<SUI>,
        pubkey_a: vector<u8>, // secp256k1 compressed
        pubkey_b: vector<u8>,
        status: u8,           // 0: OPEN, 1: CLOSING, 2: CLOSED
        state_num: u64,
        to_self_delay: u64,   // ms delay from Clock
        close_timestamp_ms: u64,
        htlcs: Table<u64, HTLC>,
        revocation_key: Option<vector<u8>>,
        revocation_hash: vector<u8>,
    }

    #[allow(unused_field)]
    public struct HTLC has store, drop {
        htlc_id: u64,
        amount: u64,
        payment_hash: vector<u8>, // sha256
        expiry: u64,              // absolute epoch
        direction: u8,            // 0: A_to_B, 1: B_to_A
        status: u8,               // 0: PENDING, 1: CLAIMED, 2: TIMEOUT
    }

    // --- Events ---

    public struct ChannelOpenEvent has copy, drop {
        channel_id: ID,
        party_a: address,
        party_b: address,
        capacity: u64,
    }

    public struct ChannelSpendEvent has copy, drop {
        channel_id: ID,
        htlc_id: u64,
        spend_type: u8, // 0: COOP, 1: FORCE, 2: CLAIM, 3: TIMEOUT, 4: PENALIZE, 5: SWEEP CLAIM
        state_num: u64,
    }

    // --- Entry Functions ---

    public fun open_channel(
        funding_coin: &mut Coin<SUI>,
        amount: u64,
        pubkey_a: vector<u8>,
        pubkey_b: vector<u8>,
        party_b: address,
        to_self_delay: u64,
        ctx: &mut TxContext
    ) {
        let party_a = tx_context::sender(ctx);
        let split_coin = coin::split(funding_coin, amount, ctx);
        let capacity = amount;
        
        // Enforce the physical minimum Timelock on-chain unconditionally (24 Hours in Production)
        assert!(to_self_delay >= MIN_TO_SELF_DELAY_MS, EDelayTooShort);
        
        let channel = Channel {
            id: object::new(ctx),
            party_a,
            party_b,
            balance_a: capacity,
            balance_b: 0,
            funding_balance: coin::into_balance(split_coin),
            pubkey_a,
            pubkey_b,
            status: 0, // OPEN
            state_num: 0,
            to_self_delay,
            close_timestamp_ms: 0,
            htlcs: table::new(ctx),
            revocation_key: option::none(),
            revocation_hash: vector::empty<u8>(),
        };

        event::emit(ChannelOpenEvent {
            channel_id: object::id(&channel),
            party_a,
            party_b,
            capacity,
        });

        transfer::share_object(channel);
    }

    // For simplicity, we'll keep the balance inside the Channel object by
    // converting the Coin into a Balance field in a future iteration.
    // For this prototype, we'll assume the funding_coin was handled.

    public fun close_channel(
        channel: &mut Channel,
        state_num: u64,
        balance_a: u64,
        balance_b: u64,
        _sig_a: vector<u8>,
        _sig_b: vector<u8>,
        ctx: &mut TxContext
    ) {
        assert!(channel.status == 0, EChannelNotOpen);
        
        let mut preimage: vector<u8> = vector::empty();
        vector::append(&mut preimage, bcs::to_bytes(&state_num));
        vector::append(&mut preimage, bcs::to_bytes(&balance_a));
        vector::append(&mut preimage, bcs::to_bytes(&balance_b));
        let sighash = hash::sha2_256(preimage);

        // Ecdsa_k1 Hash ID 1 = Sha256 strict binding (equivalent to Bitcoin's Double-SHA)
        // Since Bitcoin 2-of-2 multisig arrays are sorted lexicographically, _sig_a and _sig_b can be swapped.
        // We dynamically attempt both cryptographic combinations.
        let mut valid = false;
        if (ecdsa_k1::secp256k1_verify(&_sig_a, &channel.pubkey_a, &sighash, 1) &&
            ecdsa_k1::secp256k1_verify(&_sig_b, &channel.pubkey_b, &sighash, 1)) {
            valid = true;
        } else if (ecdsa_k1::secp256k1_verify(&_sig_b, &channel.pubkey_a, &sighash, 1) &&
                   ecdsa_k1::secp256k1_verify(&_sig_a, &channel.pubkey_b, &sighash, 1)) {
            valid = true;
        };
        assert!(valid, EInvalidSignature);
        
        
        channel.balance_a = balance_a;
        channel.balance_b = balance_b;
        channel.state_num = state_num;
        channel.status = 2; // CLOSED

        if (balance_a > 0) {
            let coin_a = coin::take(&mut channel.funding_balance, balance_a, ctx);
            transfer::public_transfer(coin_a, channel.party_a);
        };
        if (balance_b > 0) {
            let coin_b = coin::take(&mut channel.funding_balance, balance_b, ctx);
            transfer::public_transfer(coin_b, channel.party_b);
        };

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 0, // COOP
            state_num: channel.state_num,
        });
    }

    public fun force_close(
        channel: &mut Channel,
        state_num: u64,
        local_balance: u64,
        remote_balance: u64,
        revocation_hash: vector<u8>,
        commitment_sig: vector<u8>,
        htlc_ids: vector<u64>,
        htlc_amounts: vector<u64>,
        htlc_payment_hashes: vector<vector<u8>>,
        htlc_expiries: vector<u64>,
        htlc_directions: vector<u8>,
        clock: &Clock,
        _ctx: &mut TxContext
    ) {
        assert!(channel.status == 0, EChannelNotOpen);
        assert!(state_num >= channel.state_num, EInvalidStateNum);

        let len = vector::length(&htlc_ids);
        assert!(vector::length(&htlc_amounts) == len, EInvalidLength);
        assert!(vector::length(&htlc_payment_hashes) == len, EInvalidLength);
        assert!(vector::length(&htlc_expiries) == len, EInvalidLength);
        assert!(vector::length(&htlc_directions) == len, EInvalidLength);

        let mut preimage = vector::empty<u8>();
        vector::append(&mut preimage, bcs::to_bytes(&state_num));
        vector::append(&mut preimage, bcs::to_bytes(&local_balance));
        vector::append(&mut preimage, bcs::to_bytes(&remote_balance));
        vector::append(&mut preimage, bcs::to_bytes(&revocation_hash));
        vector::append(&mut preimage, bcs::to_bytes(&htlc_ids));
        vector::append(&mut preimage, bcs::to_bytes(&htlc_amounts));
        vector::append(&mut preimage, bcs::to_bytes(&htlc_payment_hashes));
        vector::append(&mut preimage, bcs::to_bytes(&htlc_expiries));
        vector::append(&mut preimage, bcs::to_bytes(&htlc_directions));
        let sighash = hash::sha2_256(preimage);

        // Dynamically deduce the broadcaster by evaluating which party's public key mathematically satisfies the signature.
        // In a unilateral close, the broadcaster possesses the OTHER party's signature.
        let mut valid = false;
        if (ecdsa_k1::secp256k1_verify(&commitment_sig, &channel.pubkey_b, &sighash, 1)) {
            // Alice broadcasted this, so local_balance is hers.
            channel.balance_a = local_balance;
            channel.balance_b = remote_balance;
            valid = true;
        } else if (ecdsa_k1::secp256k1_verify(&commitment_sig, &channel.pubkey_a, &sighash, 1)) {
            // Bob broadcasted this, so local_balance is his.
            channel.balance_b = local_balance;
            channel.balance_a = remote_balance;
            valid = true;
        };
        assert!(valid, EInvalidSignature);
        
        channel.status = 1; // CLOSING
        
        channel.close_timestamp_ms = clock::timestamp_ms(clock);
        channel.revocation_hash = revocation_hash;

        let mut i = 0;
        while (i < len) {
            let htlc_id = *vector::borrow(&htlc_ids, i);
            let htlc = HTLC {
                htlc_id,
                amount: *vector::borrow(&htlc_amounts, i),
                payment_hash: *vector::borrow(&htlc_payment_hashes, i),
                expiry: *vector::borrow(&htlc_expiries, i),
                direction: *vector::borrow(&htlc_directions, i),
                status: 0, // PENDING
            };
            table::add(&mut channel.htlcs, htlc_id, htlc);
            i = i + 1;
        };

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 1, // FORCE
            state_num: channel.state_num,
        });
    }

    #[allow(lint(self_transfer))]
    public fun claim_force_close(
        channel: &mut Channel,
        clock: &Clock,
        ctx: &mut TxContext
    ) {
        assert!(channel.status == 1, EInvalidStatus); // CLOSING
        let sender = tx_context::sender(ctx);

        if (sender == channel.party_a) {
            // Alice must wait for time lock!
            assert!(clock::timestamp_ms(clock) >= channel.close_timestamp_ms + channel.to_self_delay, ENotExpired);
            let amount = channel.balance_a;
            assert!(amount > 0, EInsufficientBalance);
            channel.balance_a = 0;
            let coin_a = coin::take(&mut channel.funding_balance, amount, ctx);
            transfer::public_transfer(coin_a, sender);
        } else if (sender == channel.party_b) {
            // Bob claims his balance immediately
            let amount = channel.balance_b;
            assert!(amount > 0, EInsufficientBalance);
            channel.balance_b = 0;
            let coin_b = coin::take(&mut channel.funding_balance, amount, ctx);
            transfer::public_transfer(coin_b, sender);
        } else {
            abort EInvalidSignature // Or EInvalidStatus if unauthorized
        };

        if (balance::value(&channel.funding_balance) == 0) {
            channel.status = 2; // CLOSED
        } else {
            channel.status = 1; // Still CLOSING, waiting for other party
        };
        
        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 5, // SWEEP CLAIM
            state_num: channel.state_num,
        });
    }

    public fun htlc_claim(
        channel: &mut Channel,
        htlc_id: u64,
        preimage: vector<u8>,
        _ctx: &mut TxContext
    ) {
        let htlc = table::borrow_mut(&mut channel.htlcs, htlc_id);
        assert!(htlc.status == 0, EInvalidStatus); // PENDING
        
        let hash = hash::sha2_256(preimage);
        assert!(hash == htlc.payment_hash, EInvalidPreimage);

        htlc.status = 1; // CLAIMED
        
        // Update balances based on direction.
        if (htlc.direction == 0) { // A to B
            channel.balance_a = channel.balance_a - htlc.amount;
            channel.balance_b = channel.balance_b + htlc.amount;
        } else { // B to A
            channel.balance_b = channel.balance_b - htlc.amount;
            channel.balance_a = channel.balance_a + htlc.amount;
        };

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id,
            spend_type: 2, // CLAIM
            state_num: channel.state_num,
        });
    }

    public fun htlc_timeout(
        channel: &mut Channel,
        htlc_id: u64,
        clock: &Clock,
        _ctx: &mut TxContext
    ) {
        let htlc = table::borrow_mut(&mut channel.htlcs, htlc_id);
        assert!(htlc.status == 0, EInvalidStatus); // PENDING
        assert!(clock::timestamp_ms(clock) >= htlc.expiry, ENotExpired);

        htlc.status = 2; // TIMEOUT

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id,
            spend_type: 3, // TIMEOUT
            state_num: channel.state_num,
        });
    }

    #[allow(lint(self_transfer))]
    public fun penalize(
        channel: &mut Channel,
        revocation_secret: vector<u8>,
        ctx: &mut TxContext
    ) {
        // Evaluate the SHA256 of the provided `revocation_secret` against the dynamically bound hash inside the channel
        let actual_hash = hash::sha2_256(revocation_secret);
        assert!(actual_hash == channel.revocation_hash, EInvalidHash);

        // If valid, confiscate all remaining state values.
        channel.balance_a = 0;
        channel.balance_b = 0;
        channel.status = 2; // CLOSED

        let remaining = balance::value(&channel.funding_balance);
        if (remaining > 0) {
            let honest_party = tx_context::sender(ctx);
            let coin_all = coin::take(&mut channel.funding_balance, remaining, ctx);
            transfer::public_transfer(coin_all, honest_party);
        };

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 4, // PENALIZE
            state_num: channel.state_num,
        });
    }
}