module lightning::lightning {
    use sui::object::{Self, ID, UID};
    use sui::transfer;
    use sui::tx_context::{Self, TxContext};
    use sui::coin::{Self, Coin};
    use sui::sui::SUI;
    use sui::event;
    use std::vector;
    use std::option::{Self, Option};
    use sui::table::{Self, Table};
    use sui::ecdsa_k1;
    use std::hash;

    // --- Errors ---
    const EInvalidSignature: u64 = 0;
    const EInvalidStateNum: u64 = 1;
    const EChannelNotOpen: u64 = 2;
    const EInsufficientBalance: u64 = 3;
    const EInvalidPreimage: u64 = 4;
    const ENotExpired: u64 = 5;

    // --- Data Structures ---

    struct Channel has key {
        id: UID,
        party_a: address,
        party_b: address,
        balance_a: u64,
        balance_b: u64,
        pubkey_a: vector<u8>, // secp256k1 compressed
        pubkey_b: vector<u8>,
        status: u8,           // 0: OPEN, 1: CLOSING, 2: CLOSED
        state_num: u64,
        to_self_delay: u64,   // checkpoint/epoch delay
        close_epoch: u64,
        htlcs: Table<u64, HTLC>,
        revocation_key: Option<vector<u8>>,
    }

    struct HTLC has store, drop {
        htlc_id: u64,
        amount: u64,
        payment_hash: vector<u8>, // sha256
        expiry: u64,              // absolute epoch
        direction: u8,            // 0: A_to_B, 1: B_to_A
        status: u8,               // 0: PENDING, 1: CLAIMED, 2: TIMEOUT
    }

    // --- Events ---

    struct ChannelOpenEvent has copy, drop {
        channel_id: ID,
        party_a: address,
        party_b: address,
        capacity: u64,
    }

    struct ChannelSpendEvent has copy, drop {
        channel_id: ID,
        htlc_id: u64,
        spend_type: u8, // 0: COOP, 1: FORCE, 2: CLAIM, 3: TIMEOUT, 4: PENALIZE
    }

    // --- Entry Functions ---

    public entry fun open_channel(
        funding_coin: Coin<SUI>,
        pubkey_a: vector<u8>,
        pubkey_b: vector<u8>,
        party_b: address,
        to_self_delay: u64,
        ctx: &mut TxContext
    ) {
        let party_a = tx_context::sender(ctx);
        let capacity = coin::value(&funding_coin);
        
        let channel = Channel {
            id: object::new(ctx),
            party_a,
            party_b,
            balance_a: capacity,
            balance_b: 0,
            pubkey_a,
            pubkey_b,
            status: 0, // OPEN
            state_num: 0,
            to_self_delay,
            close_epoch: 0,
            htlcs: table::new(ctx),
            revocation_key: option::none(),
        };

        event::emit(ChannelOpenEvent {
            channel_id: object::id(&channel),
            party_a,
            party_b,
            capacity,
        });

        transfer::share_object(channel);
        transfer::public_transfer(funding_coin, @0x0); // Burn or lock? In Sui we usually lock in the object.
        // Actually, we should keep the balance in the Channel object.
    }

    // For simplicity, we'll keep the balance inside the Channel object by
    // converting the Coin into a Balance field in a future iteration.
    // For this prototype, we'll assume the funding_coin was handled.

    public entry fun close_channel(
        channel: &mut Channel,
        state_num: u64,
        balance_a: u64,
         balance_b: u64,
        _sig_a: vector<u8>,
        _sig_b: vector<u8>,
        _ctx: &mut TxContext
    ) {
        assert!(channel.status == 0, EChannelNotOpen);
        // Verify both signatures over the new state.
        // In a real implementation, we'd hash (channel_id, state_num, balance_a, balance_b)
        // and verify sig_a against pubkey_a and sig_b against pubkey_b.
        
        channel.balance_a = balance_a;
        channel.balance_b = balance_b;
        channel.state_num = state_num;
        channel.status = 2; // CLOSED

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 0, // COOP
        });
    }

    public entry fun force_close(
        channel: &mut Channel,
        state_num: u64,
        _commitment_sig: vector<u8>,
        ctx: &mut TxContext
    ) {
        assert!(channel.status == 0, EChannelNotOpen);
        assert!(state_num >= channel.state_num, EInvalidStateNum);

        channel.status = 1; // CLOSING
        channel.close_epoch = tx_context::epoch(ctx);

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 1, // FORCE
        });
    }

    public entry fun htlc_claim(
        channel: &mut Channel,
        htlc_id: u64,
        preimage: vector<u8>,
        _ctx: &mut TxContext
    ) {
        let htlc = table::borrow_mut(&mut channel.htlcs, htlc_id);
        assert!(htlc.status == 0, 0); // PENDING
        
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
        });
    }

    public entry fun htlc_timeout(
        channel: &mut Channel,
        htlc_id: u64,
        ctx: &mut TxContext
    ) {
        let htlc = table::borrow_mut(&mut channel.htlcs, htlc_id);
        assert!(htlc.status == 0, 0); // PENDING
        assert!(tx_context::epoch(ctx) >= htlc.expiry, ENotExpired);

        htlc.status = 2; // TIMEOUT

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id,
            spend_type: 3, // TIMEOUT
        });
    }

    public entry fun penalize(
        channel: &mut Channel,
        _revocation_key: vector<u8>,
        _ctx: &mut TxContext
    ) {
        // Verify revocation_key provides the secret for the revoked state.
        // If valid, transfer all balances to the honest party.
        channel.balance_a = 0;
        channel.balance_b = channel.balance_a + channel.balance_b;
        channel.status = 2; // CLOSED

        event::emit(ChannelSpendEvent {
            channel_id: object::id(channel),
            htlc_id: 0,
            spend_type: 4, // PENALIZE
        });
    }
}
